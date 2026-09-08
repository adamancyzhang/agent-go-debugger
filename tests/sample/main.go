// Sample debug target for agent-go-debugger e2e tests.
//
// Deterministic line numbers are important: build with
//   go build -gcflags="all=-N -l"
// so breakpoints resolve to the annotated lines below.
//
//   GET /tick?i=N            -> runs Work.Step(N), returns doubled value
//   GET /status              -> JSON {ticks: N, counter: N}
//   GET /boom?crash=1        -> os.Exit(3)
//   GET /loop?n=N            -> runs Work.Loop(N) (deterministic inner loops)
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// E2E anchor lines (used by e2e_sample.sh to place file breakpoints):
// ▼bp1 - Work.Step body start
// ▼bp2 - Work.Loop inner body
// ▼bp3 - handleTick call site of Work.Step

var ticks int64

// Work carries the functions the e2e tests instrument.
type Work struct {
	Name string
}

// Step doubles i and formats a message. ▼bp1 follows on the next line.
func (w *Work) Step(i int) string {
	// ▼bp1
	doubled := i * 2
	msg := fmt.Sprintf("%s doubled %d -> %d", w.Name, i, doubled)
	history := []string{msg}
	_ = history
	return msg
}

// Loop counts from 0 to n accumulating a sum.
func (w *Work) Loop(n int) int {
	sum := 0
	for j := 0; j < n; j++ { // ▼bp2
		sum += j
		// ▼bp3
		if sum%2 == 0 {
			sum++
		}
	}
	return sum
}

func handleTick(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&ticks, 1)
	i := 0
	if v := r.URL.Query().Get("i"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			i = n
		}
	}
	work := &Work{Name: "sample"}
	msg := work.Step(i) // ▼bp4
	fmt.Fprintln(w, msg)
}

func handleLoop(w http.ResponseWriter, r *http.Request) {
	n := 5
	if v := r.URL.Query().Get("n"); v != "" {
		if m, err := strconv.Atoi(v); err == nil {
			n = m
		}
	}
	work := &Work{Name: "loop"}
	sum := work.Loop(n)
	fmt.Fprintln(w, sum)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]int64{
		"ticks":   atomic.LoadInt64(&ticks),
		"counter": atomic.LoadInt64(&tickerCounter),
	})
}

func handleBoom(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("crash") == "1" {
		os.Exit(3)
	}
	fmt.Fprintln(w, "no boom")
}

var tickerCounter int64

func main() {
	port := os.Getenv("SAMPLE_PORT")
	if port == "" {
		port = "18080"
	}
	go func() {
		for range time.Tick(500 * time.Millisecond) {
			atomic.AddInt64(&tickerCounter, 1)
		}
	}()
	http.HandleFunc("/tick", handleTick)
	http.HandleFunc("/loop", handleLoop)
	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/boom", handleBoom)
	fmt.Printf("sample listening on :%s (pid %d)\n", port, os.Getpid())
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		fmt.Fprintln(os.Stderr, "server error:", err)
		os.Exit(1)
	}
}
