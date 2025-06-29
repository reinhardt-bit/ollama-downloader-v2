package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"ollama-downloader-v2/client"
	"ollama-downloader-v2/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	logFile, err := os.OpenFile("ollama-downloader.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)

	var modelName string
	var host string

	flag.StringVar(&modelName, "model", "", "The name of the model to download (e.g., 'llama3')")
	flag.StringVar(&modelName, "m", "", "The name of the model to download (shorthand)")
	flag.StringVar(&host, "host", "", "Ollama API host (e.g., 'http://localhost:11434'). Overrides OLLAMA_HOST.")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s -model <model-name> [flags]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if modelName == "" {
		log.Println("Error: model name is required.")
		fmt.Println("Error: model name is required.")
		flag.Usage()
		os.Exit(1)
	}

	if host == "" {
		host = os.Getenv("OLLAMA_HOST")
		if host == "" {
			host = "http://localhost:11434"
		}
	}

	var continueUntilComplete bool
	var shouldQuit bool

	for {
		if shouldQuit {
			break
		}

		log.Printf("Starting download for model: %s from host: %s", modelName, host)

		ctx, cancel := context.WithCancel(context.Background())

		progressCh := make(chan tea.Msg)
		quitUICh := make(chan struct{})
		userChoiceCh := make(chan string) // Unbuffered channel

		model := ui.NewModel(modelName, host, cancel, quitUICh, userChoiceCh) // Pass userChoiceCh to UI
		p := tea.NewProgram(model)

		go client.PullModel(ctx, modelName, host, progressCh, continueUntilComplete, userChoiceCh)

		go func() {
			for msg := range progressCh {
				p.Send(msg)
			}
			select {
			case <-quitUICh: // UI already quit
			default:
				p.Send(tea.Quit())
			}
		}()

		finalModel, err := p.Run()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				log.Printf("Program exited due to context cancellation/timeout: %v\n", err)
			} else {
				log.Printf("Alas, there's been an error: %v\n", err)
				fmt.Printf("Alas, there's been an error: %v\n", err)
				os.Exit(1)
			}
		}

		cancel()

		appModel := finalModel.(ui.Model)
		selectedChoice := appModel.GetSelectedChoice()

		switch selectedChoice {
		case "Continue (until next error)":
			continueUntilComplete = false
			log.Println("Continuing download (single retry)...")
		case "Continue (until download completed)":
			continueUntilComplete = true
			log.Println("Continuing download (until complete)....")
		case "Quit":
			log.Println("Quitting download.")
			shouldQuit = true
		default:
			if continueUntilComplete {
				log.Println("Download completed successfully.")
				shouldQuit = true
			} else {
				log.Println("Download finished or unknown choice, quitting.")
				shouldQuit = true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	log.Println("Download finished.")
}
