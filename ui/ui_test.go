package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"

	"ollama-downloader-v2/client"
)

// Helper function to create a new Model for testing
func newTestModel() (Model, chan struct{}, chan string) {
	quitUICh := make(chan struct{}, 1)
	userChoiceCh := make(chan string, 1)
	cancel := func() {}
	model := NewModel("test-model", "http://localhost:11434", cancel, quitUICh, userChoiceCh)
	return model, quitUICh, userChoiceCh
}

// --- CORRECTED TEST ---
func TestModel_Init(t *testing.T) {
	m, _, _ := newTestModel()
	cmd := m.Init()
	// Init now returns a tea.Tick command to handle the ETA timer.
	// The test should confirm that a command is returned, not that it's nil.
	assert.NotNil(t, cmd, "Init should return a command to start the ticker")
}

func TestModel_Update_WindowSizeMsg(t *testing.T) {
	m, _, _ := newTestModel()
	msg := tea.WindowSizeMsg{Width: 80, Height: 20}
	updatedModel, cmd := m.Update(msg)

	assert.NotNil(t, updatedModel, "Updated model should not be nil")
	assert.Nil(t, cmd, "Update with WindowSizeMsg should return nil command")

	model := updatedModel.(Model)
	assert.Equal(t, 80-padding*2-4, model.progress.Width, "Progress bar width should be updated")
	assert.Equal(t, 80, model.list.Width(), "List width should be updated")

	// Test width clamping
	msg = tea.WindowSizeMsg{Width: 1000, Height: 20}
	updatedModel, cmd = m.Update(msg)
	model = updatedModel.(Model)
	assert.Equal(t, maxWidth, model.progress.Width, "Progress bar width should be clamped to maxWidth")
}

func TestModel_Update_KeyMsg_Quit(t *testing.T) {
	m, quitUICh, userChoiceCh := newTestModel()
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	updatedModel, cmd := m.Update(msg)

	assert.NotNil(t, updatedModel, "Updated model should not be nil")
	assert.NotNil(t, cmd, "Update with 'q' should return a command")
	assert.Equal(t, tea.Quit(), cmd(), "Command should be tea.Quit")

	model := updatedModel.(Model)
	assert.True(t, model.quitting, "Model should be in quitting state")
	assert.Equal(t, "Quit", model.selectedChoice, "Selected choice should be 'Quit'")

	select {
	case <-quitUICh:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("quitUICh was not closed")
	}
	select {
	case choice := <-userChoiceCh:
		assert.Equal(t, "Quit", choice, "User choice should be 'Quit'")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("userChoiceCh did not receive 'Quit'")
	}
}

func TestModel_Update_KeyMsg_Enter_ListShown(t *testing.T) {
	m, quitUICh, userChoiceCh := newTestModel()
	m.showList = true
	m.list.SetItems([]list.Item{item("Option 1"), item("Option 2")})
	m.list.Select(0)

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	updatedModel, cmd := m.Update(msg)

	assert.NotNil(t, updatedModel, "Updated model should not be nil")
	assert.NotNil(t, cmd, "Update with Enter should return a command")
	assert.Equal(t, tea.Quit(), cmd(), "Command should be tea.Quit")

	model := updatedModel.(Model)
	assert.Equal(t, "Option 1", model.selectedChoice, "Selected choice should be 'Option 1'")

	select {
	case <-quitUICh:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("quitUICh was not closed")
	}
	select {
	case choice := <-userChoiceCh:
		assert.Equal(t, "Option 1", choice, "User choice should be 'Option 1'")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("userChoiceCh did not receive 'Option 1'")
	}
}

func TestModel_Update_ProgressMsg(t *testing.T) {
	m, _, _ := newTestModel()
	msg := client.ProgressMsg{Status: "downloading", Completed: 50, Total: 100}
	updatedModel, cmd := m.Update(msg)

	assert.NotNil(t, updatedModel, "Updated model should not be nil")
	assert.Nil(t, cmd, "Update with ProgressMsg should return nil command")

	model := updatedModel.(Model)
	assert.Equal(t, "downloading", model.status, "Status should be updated")
	assert.InDelta(t, 0.5, model.percent, 0.001, "Percent should be updated")

	msg = client.ProgressMsg{Status: "starting", Completed: 0, Total: 0}
	updatedModel, cmd = m.Update(msg)
	model = updatedModel.(Model)
	assert.Equal(t, "starting", model.status, "Status should be updated")
	assert.InDelta(t, 0.0, model.percent, 0.001, "Percent should be 0 when total is 0")
}

func TestModel_Update_TimeoutMsg(t *testing.T) {
	m, _, _ := newTestModel()
	msg := client.TimeoutMsg{}
	updatedModel, cmd := m.Update(msg)

	assert.NotNil(t, updatedModel, "Updated model should not be nil")
	assert.Nil(t, cmd, "Update with TimeoutMsg should return nil command")

	model := updatedModel.(Model)
	assert.True(t, model.showList, "showList should be true after TimeoutMsg")
}

func TestModel_Update_ErrorMsg(t *testing.T) {
	m, quitUICh, userChoiceCh := newTestModel()
	msg := client.ErrorMsg{Err: assert.AnError}
	updatedModel, cmd := m.Update(msg)

	assert.NotNil(t, updatedModel, "Updated model should not be nil")
	assert.NotNil(t, cmd, "Update with ErrorMsg should return a command")
	assert.Equal(t, tea.Quit(), cmd(), "Command should be tea.Quit")

	model := updatedModel.(Model)
	assert.True(t, strings.Contains(model.status, "Error:"), "Status should indicate an error")
	assert.Equal(t, "Quit", model.selectedChoice, "Selected choice should be 'Quit'")

	select {
	case <-quitUICh:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("quitUICh was not closed")
	}
	select {
	case choice := <-userChoiceCh:
		assert.Equal(t, "Quit", choice, "User choice should be 'Quit'")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("userChoiceCh did not receive 'Quit'")
	}
}

func TestModel_View_ShowListFalse(t *testing.T) {
	m, _, _ := newTestModel()
	m.status = "Downloading..."
	m.percent = 0.75
	m.progress.Width = 50

	viewOutput := m.View()
	assert.True(t, strings.Contains(viewOutput, "Downloading..."), "View output should contain status")
	assert.True(t, strings.Contains(viewOutput, "75%"), "View output should contain percentage in progress bar")
}

func TestModel_View_ShowListTrue(t *testing.T) {
	m, _, _ := newTestModel()
	m.showList = true
	m.list.SetItems([]list.Item{item("Option A"), item("Option B")})
	m.list.Select(0)

	viewOutput := m.View()
	assert.True(t, strings.Contains(viewOutput, "Connection timed out or context deadline exceeded. Choose an option:"), "View output should contain list title")
	assert.True(t, strings.Contains(viewOutput, "> 1. Option A"), "View output should contain selected list item")
	assert.True(t, strings.Contains(viewOutput, "2. Option B"), "View output should contain other list item")
}

func TestModel_GetSelectedChoice(t *testing.T) {
	m, _, _ := newTestModel()
	m.selectedChoice = "Test Choice"
	assert.Equal(t, "Test Choice", m.GetSelectedChoice(), "GetSelectedChoice should return the correct choice")
}
