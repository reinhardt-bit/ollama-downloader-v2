package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

// TestPullModel_Success tests the successful download of a model.
func TestPullModel_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responses := []OllamaResponse{
			{Status: "pulling manifest"},
			{Status: "pulling layer 1", Completed: 50, Total: 100},
			{Status: "pulling layer 2", Completed: 100, Total: 100},
			{Status: "success"},
		}
		for _, res := range responses {
			json.NewEncoder(w).Encode(res)
		}
	}))
	defer server.Close()

	progressCh := make(chan tea.Msg, 5)
	userChoiceCh := make(chan string)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		PullModel(context.Background(), "test-model", server.URL, progressCh, false, userChoiceCh)
	}()

	var receivedMsgs []tea.Msg
	for msg := range progressCh {
		receivedMsgs = append(receivedMsgs, msg)
	}

	wg.Wait()

	assert.Len(t, receivedMsgs, 4, "Expected 4 progress messages")
	assert.IsType(t, ProgressMsg{}, receivedMsgs[0])
	assert.Equal(t, "pulling manifest", receivedMsgs[0].(ProgressMsg).Status)
	assert.IsType(t, ProgressMsg{}, receivedMsgs[1])
	assert.Equal(t, int64(50), receivedMsgs[1].(ProgressMsg).Completed)
	assert.IsType(t, ProgressMsg{}, receivedMsgs[2])
	assert.Equal(t, int64(100), receivedMsgs[2].(ProgressMsg).Completed)
	assert.IsType(t, ProgressMsg{}, receivedMsgs[3])
	assert.Equal(t, "success", receivedMsgs[3].(ProgressMsg).Status)
}

// TestPullModel_ServerError tests the handling of a non-200 status code from the server.
func TestPullModel_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		PullModel(context.Background(), "test-model", server.URL, progressCh, false, userChoiceCh)
	}()

	msg := <-progressCh
	wg.Wait()

	assert.IsType(t, ErrorMsg{}, msg)
	assert.Error(t, msg.(ErrorMsg).Err)
	assert.Contains(t, msg.(ErrorMsg).Err.Error(), "ollama API returned status 500")
}

// TestPullModel_TimeoutAndQuit tests the timeout scenario where the user chooses to quit.
func TestPullModel_TimeoutAndQuit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond) // Simulate a delay to cause a timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// We use a custom client with a short timeout for this test
	// Note: In a real-world scenario, it's better to inject the client as a dependency
	// to avoid modifying the global DefaultClient.
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Timeout: 50 * time.Millisecond}
	defer func() { http.DefaultClient = originalClient }()

	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		PullModel(context.Background(), "test-model", server.URL, progressCh, false, userChoiceCh)
	}()

	// Expect a timeout message
	msg, ok := <-progressCh
	assert.True(t, ok, "Should receive a timeout message")
	assert.IsType(t, TimeoutMsg{}, msg)

	// Simulate user choosing to quit
	userChoiceCh <- "Quit"

	// The progress channel should be closed without further messages
	_, ok = <-progressCh
	assert.False(t, ok, "Expected progress channel to be closed")

	wg.Wait()
}

// TestPullModel_ContextCancellation tests that the function exits gracefully when the context is canceled.
func TestPullModel_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slow response to allow for cancellation
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	progressCh := make(chan tea.Msg, 1)
	userChoiceCh := make(chan string)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		PullModel(ctx, "test-model", server.URL, progressCh, false, userChoiceCh)
	}()

	// Cancel the context after a short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	// The progress channel should be closed without sending any messages
	_, ok := <-progressCh
	assert.False(t, ok, "Expected progress channel to be closed on context cancellation")

	wg.Wait()
}

// TestPullModel_UserQuitDuringDownload tests the scenario where the user quits in the middle of a download.
func TestPullModel_UserQuitDuringDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stream responses slowly
		responses := []OllamaResponse{
			{Status: "pulling manifest"},
			{Status: "downloading", Completed: 25, Total: 100},
		}
		for _, res := range responses {
			json.NewEncoder(w).Encode(res)
			// Flush to make sure the client receives the data immediately
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
		// Keep the connection open to simulate an ongoing download
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	progressCh := make(chan tea.Msg, 5)
	userChoiceCh := make(chan string, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		PullModel(context.Background(), "test-model", server.URL, progressCh, false, userChoiceCh)
	}()

	// Receive the first message
	<-progressCh
	// Receive the second message
	<-progressCh

	// Simulate user quitting
	userChoiceCh <- "Quit"

	// The progress channel should be closed
	_, ok := <-progressCh
	assert.False(t, ok, "Expected progress channel to be closed after user quits")

	wg.Wait()
}
