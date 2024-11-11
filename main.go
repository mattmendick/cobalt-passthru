package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	client = &http.Client{}

	// Define Prometheus metrics
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cobalt_passthru_http_requests_total",
			Help: "Total number of HTTP requests to the service",
		},
		[]string{"path", "cache_status"},
	)

	externalServiceRequestsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "cobalt_passthru_external_service_requests_total",
			Help: "Total number of HTTP requests to the external service",
		},
	)

	cleanupsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "cobalt_passthru_cleanups_total",
			Help: "Total number of cleanup operations run",
		},
	)

	filesCleanedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "cobalt_passthru_files_cleaned_total",
			Help: "Total number of files cleaned up",
		},
	)
)

type ExternalServiceRequest struct {
	URL             string `json:"url"`
	VideoQuality    string `json:"videoQuality"`
	DisableMetadata bool   `json:"disableMetadata"`
}

type ExternalServiceResponse struct {
	Status   string `json:"status"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
}

func initMetrics() {
	paths := []string{"/"} // Add more paths if needed
	cacheStatuses := []string{"cached", "not_cached"}

	for _, path := range paths {
		for _, cacheStatus := range cacheStatuses {
			httpRequestsTotal.WithLabelValues(path, cacheStatus).Add(0)
		}
	}
}

func main() {
	// Register Prometheus metrics
	prometheus.MustRegister(httpRequestsTotal)
	prometheus.MustRegister(externalServiceRequestsTotal)
	prometheus.MustRegister(cleanupsTotal)
	prometheus.MustRegister(filesCleanedTotal)

	// Initialize all label values
	initMetrics()

	// Define command-line flags
	endpointFlag := flag.String("endpoint", "http://external-service-endpoint", "The endpoint of the external service")
	addrFlag := flag.String("addr", ":8080", "The address and port on which the server listens")
	metricsAddrFlag := flag.String("metrics-addr", ":8081", "The address and port for serving Prometheus metrics")
	storageDirFlag := flag.String("storage", "./storage", "The directory to store files")
	flag.Parse()

	// Create the storage directory if it does not exist
	if err := os.MkdirAll(*storageDirFlag, os.ModePerm); err != nil {
		log.Fatalf("ts=%s msg=Failed_to_create_storage_directory error=%v\n", time.Now().Format(time.RFC3339), err)
	}

	// Start the file cleanup routine
	go startFileCleanupRoutine(*storageDirFlag)

	// Set up the router for the application server
	router := mux.NewRouter()
	router.HandleFunc("/", handleRequest(*endpointFlag, *storageDirFlag)).Methods("GET")
	http.Handle("/", router)

	// Start the main application server
	go func() {
		serverAddr := *addrFlag
		log.Printf("ts=%s msg=Starting_server addr=%s endpoint=%s storage=%s\n", time.Now().Format(time.RFC3339), serverAddr, *endpointFlag, *storageDirFlag)
		if err := http.ListenAndServe(serverAddr, nil); err != nil {
			log.Printf("ts=%s msg=Server_failed_to_start error=%v\n", time.Now().Format(time.RFC3339), err)
			os.Exit(1)
		}
	}()

	// Set up a separate server for Prometheus metrics
	go func() {
		metricsRouter := http.NewServeMux()
		metricsRouter.Handle("/metrics", promhttp.Handler())

		metricsAddr := *metricsAddrFlag
		log.Printf("ts=%s msg=Starting_metrics_server addr=%s\n", time.Now().Format(time.RFC3339), metricsAddr)
		if err := http.ListenAndServe(metricsAddr, metricsRouter); err != nil {
			log.Printf("ts=%s msg=Metrics_server_failed_to_start error=%v\n", time.Now().Format(time.RFC3339), err)
			os.Exit(1)
		}
	}()

	// Block the main goroutine
	select {}
}

func handleRequest(externalServiceEndpoint, storageDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		queryParams := r.URL.Query()
		url := queryParams.Get("u")
		if url == "" {
			log.Printf("ts=%s msg=Missing_query_param param=u\n", time.Now().Format(time.RFC3339))
			http.Error(w, "'u' parameter is required", http.StatusBadRequest)
			return
		}

		log.Printf("ts=%s msg=Request_received method=GET u=%s\n", start.Format(time.RFC3339), url)

		// Hash the URL to create a unique file name
		hash := sha256.Sum256([]byte(url))
		hashStr := fmt.Sprintf("%x", hash)
		binaryFileName := filepath.Join(storageDir, hashStr+".bin")
		headersFileName := filepath.Join(storageDir, hashStr+".headers")

		// Check if the files already exist
		if _, err := os.Stat(binaryFileName); err == nil {
			if _, err := os.Stat(headersFileName); err == nil {
				// Serve files directly from disk if they exist
				log.Printf("ts=%s msg=Serving_cached_file filename=%s\n", time.Now().Format(time.RFC3339), binaryFileName)
				serveBinaryFile(w, r, binaryFileName, headersFileName)
				httpRequestsTotal.WithLabelValues(r.URL.Path, "cached").Inc()
				duration := time.Since(start)
				log.Printf("ts=%s msg=Request_processed_from_cache duration=%s\n", time.Now().Format(time.RFC3339), duration)
				return
			}
		}

		// Increment HTTP requests metric for incoming non-cached request
		httpRequestsTotal.WithLabelValues(r.URL.Path, "not_cached").Inc()

		// Create request payload for the external service
		requestPayload := ExternalServiceRequest{
			URL:             url,
			VideoQuality:    "max",
			DisableMetadata: true,
		}

		reqBody, err := json.Marshal(requestPayload)
		if err != nil {
			log.Printf("ts=%s msg=Failed_JSON_marshal error=%v\n", time.Now().Format(time.RFC3339), err)
			http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
			return
		}

		// Increment external service requests metric
		externalServiceRequestsTotal.Inc()

		// Send POST request to the external service
		req, err := http.NewRequest("POST", externalServiceEndpoint, strings.NewReader(string(reqBody)))
		if err != nil {
			log.Printf("ts=%s msg=Failed_create_request error=%v\n", time.Now().Format(time.RFC3339), err)
			http.Error(w, "Failed to create request", http.StatusInternalServerError)
			return
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		log.Printf("ts=%s msg=External_service_request method=POST endpoint=%s\n", time.Now().Format(time.RFC3339), externalServiceEndpoint)

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("ts=%s msg=External_service_failure error=%v\n", time.Now().Format(time.RFC3339), err)
			http.Error(w, "Failed to call external service", http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Printf("ts=%s msg=External_service_non_200 status_code=%d\n", time.Now().Format(time.RFC3339), resp.StatusCode)
			http.Error(w, "Error from external service", http.StatusInternalServerError)
			return
		}

		var serviceResp ExternalServiceResponse
		if err := json.NewDecoder(resp.Body).Decode(&serviceResp); err != nil {
			log.Printf("ts=%s msg=Failed_JSON_decode error=%v\n", time.Now().Format(time.RFC3339), err)
			http.Error(w, "Failed to decode JSON response", http.StatusInternalServerError)
			return
		}

		// Download the binary resource
		resourceResp, err := http.Get(serviceResp.URL)
		if err != nil {
			log.Printf("ts=%s msg=Download_failure error=%v\n", time.Now().Format(time.RFC3339), err)
			http.Error(w, "Failed to download resource", http.StatusInternalServerError)
			return
		}
		defer resourceResp.Body.Close()

		// Store the resource binary
		binaryFile, err := os.Create(binaryFileName)
		if err != nil {
			log.Printf("ts=%s msg=Create_binary_file_error filename=%s error=%v\n", time.Now().Format(time.RFC3339), binaryFileName, err)
			http.Error(w, "Failed to save binary file", http.StatusInternalServerError)
			return
		}
		defer binaryFile.Close()

		_, err = io.Copy(binaryFile, resourceResp.Body)
		if err != nil {
			log.Printf("ts=%s msg=Write_binary_file_error filename=%s error=%v\n", time.Now().Format(time.RFC3339), binaryFileName, err)
			http.Error(w, "Failed to write binary file", http.StatusInternalServerError)
			return
		}

		// Store response headers
		headersFile, err := os.Create(headersFileName)
		if err != nil {
			log.Printf("ts=%s msg=Create_headers_file_error filename=%s error=%v\n", time.Now().Format(time.RFC3339), headersFileName, err)
			http.Error(w, "Failed to save headers file", http.StatusInternalServerError)
			return
		}
		defer headersFile.Close()

		for key, values := range resourceResp.Header {
			for _, value := range values {
				headersFile.WriteString(fmt.Sprintf("%s: %s\n", key, value))
			}
		}

		log.Printf("ts=%s msg=Resource_stored binary_file=%s headers_file=%s\n", time.Now().Format(time.RFC3339), binaryFileName, headersFileName)

		serveBinaryFile(w, r, binaryFileName, headersFileName)

		duration := time.Since(start)
		log.Printf("ts=%s msg=Request_processed duration=%s\n", time.Now().Format(time.RFC3339), duration)
	}
}

func serveBinaryFile(w http.ResponseWriter, r *http.Request, binaryFileName, headersFileName string) {
	headersFile, err := os.Open(headersFileName)
	if err != nil {
		log.Printf("ts=%s msg=Open_headers_file_error filename=%s error=%v\n", time.Now().Format(time.RFC3339), headersFileName, err)
		http.Error(w, "Failed to open headers file", http.StatusInternalServerError)
		return
	}
	defer headersFile.Close()

	headersBuffer := make([]byte, 1024)
	n, err := headersFile.Read(headersBuffer)
	if err != nil && err != io.EOF {
		log.Printf("ts=%s msg=Read_headers_file_error error=%v\n", time.Now().Format(time.RFC3339), err)
		http.Error(w, "Failed to read headers file", http.StatusInternalServerError)
		return
	}

	headersStr := string(headersBuffer[:n])
	headers := strings.Split(headersStr, "\n")
	for _, header := range headers {
		if header == "" {
			continue
		}
		headerParts := strings.SplitN(header, ": ", 2)
		if len(headerParts) == 2 {
			w.Header().Set(headerParts[0], headerParts[1])
		}
	}

	log.Printf("ts=%s msg=Serving_binary_file filename=%s\n", time.Now().Format(time.RFC3339), binaryFileName)
	http.ServeFile(w, r, binaryFileName)
}

func startFileCleanupRoutine(storageDir string) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			log.Printf("ts=%s msg=Starting_file_cleanup\n", time.Now().Format(time.RFC3339))
			cleanupsTotal.Inc() // Increment cleanups metric
			cleanupOldFiles(storageDir)
		}
	}
}

func cleanupOldFiles(storageDir string) {
	files, err := os.ReadDir(storageDir)
	if err != nil {
		log.Printf("ts=%s msg=Read_storage_directory_error dir=%s error=%v\n", time.Now().Format(time.RFC3339), storageDir, err)
		return
	}

	cutoff := time.Now().Add(-720 * time.Minute)

	for _, file := range files {
		filePath := filepath.Join(storageDir, file.Name())
		info, err := os.Stat(filePath)
		if err != nil {
			log.Printf("ts=%s msg=File_stat_error file=%s error=%v\n", time.Now().Format(time.RFC3339), filePath, err)
			continue
		}

		if info.ModTime().Before(cutoff) {
			err = os.Remove(filePath)
			if err != nil {
				log.Printf("ts=%s msg=File_deletion_error file=%s error=%v\n", time.Now().Format(time.RFC3339), filePath, err)
			} else {
				log.Printf("ts=%s msg=File_deleted file=%s\n", time.Now().Format(time.RFC3339), filePath)
				filesCleanedTotal.Inc() // Increment files cleaned metric
			}
		}
	}
}
