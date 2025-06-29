package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Mock HTTP server for Ollama API
func createMockOllamaServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestPullModel_Success(t *testing.T) {
	// Mock server for a successful download
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" {
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req PullRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Model != "test-model" || !req.Stream {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		// Simulate streaming progress
		progress := []OllamaResponse{
			{Status: "downloading", Completed: 10, Total: 100},
			{Status: "downloading", Completed: 50, Total: 100},
			{Status: "downloading", Completed: 90, Total: 100},
			{Status: "success", Completed: 100, Total: 100},
		}

		for _, p := range progress {
			json.NewEncoder(w).Encode(p)
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond) // Simulate network delay
		}
	})

	progressCh := make(chan tea.Msg, 10) // Buffered channel
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	// Collect messages
	var receivedMsgs []tea.Msg
	for msg := range progressCh {
		receivedMsgs = append(receivedMsgs, msg)
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			break // Exit when success message is received
		}
	}

	if len(receivedMsgs) < 4 {
		t.Fatalf("Expected at least 4 progress messages, got %d", len(receivedMsgs))
	}

	// Verify messages
	expectedStatuses := []string{"downloading", "downloading", "downloading", "success"}
	for i, msg := range receivedMsgs {
		pMsg, ok := msg.(ProgressMsg)
		if !ok {
			t.Errorf("Expected ProgressMsg, got %T", msg)
			continue
		}
		if pMsg.Status != expectedStatuses[i] {
			t.Errorf("Expected status %s, got %s at index %d", expectedStatuses[i], pMsg.Status, i)
		}
	}
}

func TestPullModel_Timeout(t *testing.T) {
	// Mock server that times out after a short delay
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // Simulate a delay longer than the client's request timeout
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "{}")
	})

	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	select {
	case msg := <-progressCh:
		_, ok := msg.(TimeoutMsg)
		if !ok {
			t.Errorf("Expected TimeoutMsg, got %T", msg)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for TimeoutMsg")
	}

	// Simulate user choosing to quit
	userChoiceCh <- "Quit"

	select {
	case msg := <-progressCh:
		t.Errorf("Did not expect any more messages after Quit, got %T", msg)
	case <-time.After(100 * time.Millisecond):
		// Expected: channel should be closed and no more messages
	}
}

func TestPullModel_APIError(t *testing.T) {
	// Mock server that returns a 500 error
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	})

	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	select {
	case msg := <-progressCh:
		errMsg, ok := msg.(ErrorMsg)
		if !ok {
			t.Errorf("Expected ErrorMsg, got %T", msg)
		}
		if !strings.Contains(errMsg.Err.Error(), "ollama API returned status 500") {
			t.Errorf("Expected API error message, got: %v", errMsg.Err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for ErrorMsg")
	}
}

func TestPullModel_ContextCancellation(t *testing.T) {
	// Mock server that streams indefinitely
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 100; i++ {
			json.NewEncoder(w).Encode(OllamaResponse{Status: "downloading", Completed: int64(i), Total: 100})
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	})

	progressCh := make(chan tea.Msg, 10)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	// Wait for some progress messages
	time.Sleep(50 * time.Millisecond)

	// Cancel the context
	cancel()

	// Ensure the progress channel is closed after cancellation
	select {
	case _, ok := <-progressCh:
		if ok {
			t.Fatal("Progress channel should be closed after context cancellation")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for progress channel to close")
	}
}

func TestPullModel_ContinueUntilComplete(t *testing.T) {
	callCount := 0
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call times out
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "{}")
		} else {
			// Subsequent calls succeed
			progress := []OllamaResponse{
				{Status: "downloading", Completed: 10, Total: 100},
				{Status: "success", Completed: 100, Total: 100},
			}
			for _, p := range progress {
				json.NewEncoder(w).Encode(p)
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
			}
		}
	})

	progressCh := make(chan tea.Msg, 10)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, true, userChoiceCh) // Set continueUntilComplete to true

	var receivedMsgs []tea.Msg
	for msg := range progressCh {
		receivedMsgs = append(receivedMsgs, msg)
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			break
		}
	}

	if callCount != 2 {
		t.Errorf("Expected 2 server calls (1 timeout, 1 success), got %d", callCount)
	}

	foundSuccess := false
	for _, msg := range receivedMsgs {
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			foundSuccess = true
			break
		}
	}
	if !foundSuccess {
		t.Error("Expected to receive a success message")
	}
}

func TestPullModel_UserChoiceContinue(t *testing.T) {
	callCount := 0
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call times out
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "{}")
		} else {
			// Subsequent calls succeed
			progress := []OllamaResponse{
				{Status: "downloading", Completed: 10, Total: 100},
				{Status: "success", Completed: 100, Total: 100},
			}
			for _, p := range progress {
				json.NewEncoder(w).Encode(p)
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
			}
		}
	})

	progressCh := make(chan tea.Msg, 10)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	// Expect TimeoutMsg first
	select {
	case msg := <-progressCh:
		_, ok := msg.(TimeoutMsg)
		if !ok {
			t.Fatalf("Expected TimeoutMsg, got %T", msg)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for TimeoutMsg")
	}

	// Simulate user choosing to continue (single retry)
	userChoiceCh <- "Continue (until next error)"

	// Now expect success messages
	var receivedMsgs []tea.Msg
	for msg := range progressCh {
		receivedMsgs = append(receivedMsgs, msg)
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			break
		}
	}

	if callCount != 2 {
		t.Errorf("Expected 2 server calls (1 timeout, 1 success), got %d", callCount)
	}

	foundSuccess := false
	for _, msg := range receivedMsgs {
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			foundSuccess = true
			break
		}
	}
	if !foundSuccess {
		t.Error("Expected to receive a success message after user chose to continue")
	}
}

func TestPullModel_UserChoiceContinueUntilComplete(t *testing.T) {
	callCount := 0
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call times out
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "{}")
		} else {
			// Subsequent calls succeed
			progress := []OllamaResponse{
				{Status: "downloading", Completed: 10, Total: 100},
				{Status: "success", Completed: 100, Total: 100},
			}
			for _, p := range progress {
				json.NewEncoder(w).Encode(p)
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
			}
		}
	})

	progressCh := make(chan tea.Msg, 10)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	// Expect TimeoutMsg first
	select {
	case msg := <-progressCh:
		_, ok := msg.(TimeoutMsg)
		if !ok {
			t.Fatalf("Expected TimeoutMsg, got %T", msg)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for TimeoutMsg")
	}

	// Simulate user choosing to continue until complete
	userChoiceCh <- "Continue (until download completed)"

	// Now expect success messages
	var receivedMsgs []tea.Msg
	for msg := range progressCh {
		receivedMsgs = append(receivedMsgs, msg)
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			break
		}
	}

	if callCount != 2 {
		t.Errorf("Expected 2 server calls (1 timeout, 1 success), got %d", callCount)
	}

	foundSuccess := false
	for _, msg := range receivedMsgs {
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			foundSuccess = true
			break
		}
	}
	if !foundSuccess {
		t.Error("Expected to receive a success message after user chose to continue until complete")
	}
}

func TestPullModel_UserChoiceQuit(t *testing.T) {
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // Simulate a delay longer than the client's request timeout
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "{}")
	})

	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	select {
	case msg := <-progressCh:
		_, ok := msg.(TimeoutMsg)
		if !ok {
			t.Errorf("Expected TimeoutMsg, got %T", msg)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for TimeoutMsg")
	}

	// Simulate user choosing to quit
	userChoiceCh <- "Quit"

	// The progress channel should be closed shortly after the user chooses to quit
	select {
	case _, ok := <-progressCh:
		if ok {
			t.Fatal("Progress channel should be closed after user chooses to quit")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for progress channel to close after Quit")
	}
}

func TestPullModel_InvalidJSONResponse(t *testing.T) {
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "this is not json")
		w.(http.Flusher).Flush()
		fmt.Fprintln(w, `{"status": "success"}`) // Valid JSON after invalid
		w.(http.Flusher).Flush()
	})

	progressCh := make(chan tea.Msg, 10)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	var receivedMsgs []tea.Msg
	for msg := range progressCh {
		receivedMsgs = append(receivedMsgs, msg)
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			break
		}
	}

	// We should still receive the success message despite the invalid JSON
	foundSuccess := false
	for _, msg := range receivedMsgs {
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			foundSuccess = true
			break
		}
	}
	if !foundSuccess {
		t.Error("Expected to receive a success message despite invalid JSON in stream")
	}
}

func TestPullModel_RequestCreationError(t *testing.T) {
	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use an invalid host to trigger a request creation error
	go PullModel(ctx, "test-model", "http://invalid-host:invalid-port", progressCh, false, userChoiceCh)

	select {
	case msg := <-progressCh:
		errMsg, ok := msg.(ErrorMsg)
		if !ok {
			t.Errorf("Expected ErrorMsg, got %T", msg)
		}
		if !strings.Contains(errMsg.Err.Error(), "error creating request") {
			t.Errorf("Expected request creation error message, got: %v", errMsg.Err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for ErrorMsg")
	}
}

func TestPullModel_ReadResponseBodyError(t *testing.T) {
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Close the connection prematurely to simulate read error
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "Hijacker not supported", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		conn.Close()
	})

	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	select {
	case msg := <-progressCh:
		errMsg, ok := msg.(ErrorMsg)
		if !ok {
			t.Errorf("Expected ErrorMsg, got %T", msg)
		}
		if !strings.Contains(errMsg.Err.Error(), "error reading response") && !strings.Contains(errMsg.Err.Error(), "EOF") {
			t.Errorf("Expected read response error message, got: %v", errMsg.Err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for ErrorMsg")
	}
}

func TestPullModel_InitialRequestTimeout(t *testing.T) {
	// Mock server that delays the initial response
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(35 * time.Second) // Longer than the 30-second request timeout
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status": "success"}`)
	})

	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	select {
	case msg := <-progressCh:
		_, ok := msg.(TimeoutMsg)
		if !ok {
			t.Errorf("Expected TimeoutMsg, got %T", msg)
		}
	case <-time.After(31 * time.Second): // Wait slightly longer than the client's timeout
		t.Fatal("Test timed out waiting for TimeoutMsg")
	}

	// Simulate user choosing to quit
	userChoiceCh <- "Quit"

	select {
	case msg := <-progressCh:
		t.Errorf("Did not expect any more messages after Quit, got %T", msg)
	case <-time.After(100 * time.Millisecond):
		// Expected: channel should be closed and no more messages
	}
}

func TestPullModel_StreamReadingTimeout(t *testing.T) {
	// Mock server that sends some data then delays indefinitely
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(OllamaResponse{Status: "downloading", Completed: 10, Total: 100})
		w.(http.Flusher).Flush()
		time.Sleep(35 * time.Second) // Longer than the 30-second request timeout
		json.NewEncoder(w).Encode(OllamaResponse{Status: "success", Completed: 100, Total: 100})
		w.(http.Flusher).Flush()
	})

	progressCh := make(chan tea.Msg, 10)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	// Wait for the initial progress message
	select {
	case msg := <-progressCh:
		pMsg, ok := msg.(ProgressMsg)
		if !ok || pMsg.Status != "downloading" {
			t.Fatalf("Expected initial ProgressMsg, got %T %+v", msg, msg)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for initial ProgressMsg")
	}

	// Now wait for the timeout message
	select {
	case msg := <-progressCh:
		_, ok := msg.(TimeoutMsg)
		if !ok {
			t.Errorf("Expected TimeoutMsg, got %T", msg)
		}
	case <-time.After(31 * time.Second): // Wait slightly longer than the client's timeout
		t.Fatal("Test timed out waiting for TimeoutMsg")
	}

	// Simulate user choosing to quit
	userChoiceCh <- "Quit"

	select {
	case msg := <-progressCh:
		t.Errorf("Did not expect any more messages after Quit, got %T", msg)
	case <-time.After(100 * time.Millisecond):
		// Expected: channel should be closed and no more messages
	}
}

func TestPullModel_UnmarshallingError(t *testing.T) {
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Send malformed JSON
		fmt.Fprintln(w, `{"status": "downloading", "completed": "not-a-number", "total": 100}`)
		w.(http.Flusher).Flush()
		fmt.Fprintln(w, `{"status": "success", "completed": 100, "total": 100}`) // Valid JSON after malformed
		w.(http.Flusher).Flush()
	})

	progressCh := make(chan tea.Msg, 10)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	var receivedMsgs []tea.Msg
	for msg := range progressCh {
		receivedMsgs = append(receivedMsgs, msg)
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			break
		}
	}

	// We should still receive the success message despite the unmarshalling error
	foundSuccess := false
	for _, msg := range receivedMsgs {
		if pMsg, ok := msg.(ProgressMsg); ok && pMsg.Status == "success" {
			foundSuccess = true
			break
		}
	}
	if !foundSuccess {
		t.Error("Expected to receive a success message despite unmarshalling error in stream")
	}
}

func TestPullModel_EmptyResponse(t *testing.T) {
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Send an empty response body
	})

	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	// Since the response is empty and no "success" message is sent, it should eventually timeout
	select {
	case msg := <-progressCh:
		_, ok := msg.(TimeoutMsg)
		if !ok {
			t.Errorf("Expected TimeoutMsg, got %T", msg)
		}
	case <-time.After(1 * time.Second): // This timeout is for the test, not the client's internal timeout
		t.Fatal("Test timed out waiting for TimeoutMsg")
	}

	// Simulate user choosing to quit
	userChoiceCh <- "Quit"

	select {
	case msg := <-progressCh:
		t.Errorf("Did not expect any more messages after Quit, got %T", msg)
	case <-time.After(100 * time.Millisecond):
		// Expected: channel should be closed and no more messages
	}
}

func TestPullModel_NonStreamedResponse(t *testing.T) {
	server := createMockOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req PullRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			// If the client requests streaming, but we send a non-streamed response
			json.NewEncoder(w).Encode(OllamaResponse{Status: "success", Completed: 100, Total: 100})
		} else {
			http.Error(w, "Expected streamed request", http.StatusBadRequest)
		}
	})

	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)

	select {
	case msg := <-progressCh:
		pMsg, ok := msg.(ProgressMsg)
		if !ok {
			t.Errorf("Expected ProgressMsg, got %T", msg)
		}
		if pMsg.Status != "success" {
			t.Errorf("Expected status 'success', got '%s'", pMsg.Status)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for ProgressMsg")
	}
}
