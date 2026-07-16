// Command mock-ingest 是「等後端」期間的極簡 mock 上傳端點（§7）。
//
// 預設回 200；設環境變數 ECO_AGENT_MOCK_RESPONSE_STATUS=401（或 403）可測試 Eco-Agent
// 的撤銷自清路徑。監聽位址預設 :8080，端點路徑 /mock/ingest（對齊 DefaultUploadURL）。
//
// 執行：
//
//	go run ./cmd/mock-ingest
//	ECO_AGENT_MOCK_RESPONSE_STATUS=401 go run ./cmd/mock-ingest   # 測撤銷
package main

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"eco-agent/internal/uploader"
)

const (
	envStatus = "ECO_AGENT_MOCK_RESPONSE_STATUS"
	envAddr   = "ECO_AGENT_MOCK_ADDR"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	status := http.StatusOK
	if v := os.Getenv(envStatus); v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			status = s
		} else {
			log.Warn("invalid status env, using 200", "value", v)
		}
	}
	addr := os.Getenv(envAddr)
	if addr == "" {
		addr = ":8080"
	}

	srv := uploader.NewMockIngestServer(status)
	handler := logRequests(log, srv)

	log.Info("mock-ingest listening", "addr", addr, "path", "/mock/ingest", "response_status", status)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Error("mock-ingest server stopped", "err", err)
		os.Exit(1)
	}
}

// logRequests 於每次請求後印出目前收到的事件總數，便於觀察 mock 端點收到資料。
func logRequests(log *slog.Logger, srv *uploader.MockIngestServer) http.Handler {
	inner := srv.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		if r.URL.Path == "/mock/ingest" {
			log.Info("ingest request", "method", r.Method, "total_events", srv.EventCount())
		}
	})
}
