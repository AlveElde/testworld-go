package testworld

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type WorldLog struct {
	rw              sync.RWMutex
	world           *World
	combinedLog     io.WriteCloser
	combinedLogPath string
	eventsDir       string
	events          []*Event
	startTime       time.Time
	finishTime      time.Time
	eventCounter    int64
}

type Event struct {
	world       *World
	id          int64
	description string
	startTime   time.Time
	finishTime  time.Time
	log         io.WriteCloser
}

func NewWorldLog(world *World, path string) (*WorldLog, error) {
	var el WorldLog
	el.world = world
	el.startTime = time.Now()

	// Ensure we have a logs directory.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absPath, 0755); err != nil {
		return nil, err
	}

	// Create the event log file.
	el.combinedLogPath = filepath.Join(absPath, "log_"+el.world.name+"_events.log")
	el.combinedLog, err = os.Create(el.combinedLogPath)
	if err != nil {
		return nil, err
	}

	// Create a directory for temporary event logs. Each event writes to its
	// own log file, and the files are concatenated at the end of the test.
	el.eventsDir = filepath.Join(world.t.TempDir(), "events")
	if err = os.MkdirAll(el.eventsDir, 0755); err != nil {
		return nil, err
	}
	return &el, nil
}

// finish finalizes the world log by writing a Gantt chart and concatenating all
// event logs into the main log file.
func (el *WorldLog) finish() error {
	if el == nil || el.world == nil {
		return nil
	}

	defer el.combinedLog.Close()
	el.finishTime = time.Now()

	el.printGantt()

	// Concatenate all the event logs into the main event log.
	fmt.Fprintln(el.combinedLog, "\n\nEvent Logs:")
	eventLogs, err := os.ReadDir(el.eventsDir)
	if err != nil {
		return fmt.Errorf("failed to read event logs directory: %w", err)
	}
	for _, eventLogFile := range eventLogs {
		f, err := os.Open(filepath.Join(el.eventsDir, eventLogFile.Name()))
		if err != nil {
			return fmt.Errorf("failed to open event log file %s: %w", eventLogFile.Name(), err)
		}
		defer f.Close()

		_, err = io.Copy(el.combinedLog, f)
		if err != nil {
			return fmt.Errorf("failed to copy event log file %s: %w", eventLogFile.Name(), err)
		}
	}

	el.world.t.Log("World destroyed, event logs written to ", el.combinedLogPath)
	return nil
}

// printGantt writes a simple ASCII Gantt chart to the world log.
func (el *WorldLog) printGantt() {
	if el == nil || el.world == nil {
		return
	}

	totalDuration := el.finishTime.Sub(el.startTime).Seconds()
	fmt.Fprintf(el.combinedLog, "Event Timeline (Total: %.3fs):\n", totalDuration)
	fmt.Fprintln(el.combinedLog, "ID  | Process Visualization")
	fmt.Fprintln(el.combinedLog, "----|"+strings.Repeat("-", timelineWidth))

	for _, e := range el.events {
		// Calculate offset and bar length.
		// If the event was never finished (e.g., test failed mid-way),
		// treat it as running until the world was destroyed.
		finishTime := e.finishTime
		if finishTime.IsZero() {
			finishTime = el.finishTime
		}
		startOffset := e.startTime.Sub(el.startTime).Seconds()
		duration := finishTime.Sub(e.startTime).Seconds()

		startPos := max(int((startOffset/totalDuration)*timelineWidth), 0)
		barLen := max(int((duration/totalDuration)*timelineWidth), 1)

		padding := strings.Repeat(" ", startPos)
		bar := strings.Repeat("#", barLen)

		fmt.Fprintf(el.combinedLog, "%03d |%s[%s] (%.3fs) %s\n",
			e.id, padding, bar, duration, e.description)
	}
}

// newEvent logs the start of an event to the world log and returns an Event.
func (el *WorldLog) newEvent(fomat string, args ...any) *Event {
	if el == nil || el.world == nil {
		return nil
	}

	now := time.Now()

	// Create and store a new event
	el.rw.Lock()
	event := &Event{
		world:     el.world,
		id:        el.eventCounter,
		startTime: now,
	}
	el.events = append(el.events, event)
	el.eventCounter++
	el.rw.Unlock()

	event.description = fmt.Sprintf(fomat, args...)
	message := fmt.Sprintf("Event %03d start:  %s", event.id, event.description)

	// Create a new temporary log file for the event.
	var err error
	event.log, err = os.Create(filepath.Join(el.eventsDir, fmt.Sprintf("event_%03d.log", event.id)))
	if err != nil {
		return nil
	}

	fmt.Fprintln(event.log, message)

	// Log to the go test log as well
	event.world.t.Helper()
	event.world.t.Log(message)

	return event
}

// finishEvent logs the end of an event to the world log.
func (event *Event) finish() {
	if event == nil {
		return
	}

	event.finishTime = time.Now()
	duration := event.finishTime.Sub(event.startTime).Seconds()
	message := fmt.Sprintf("Event %03d finish: duration %.3fs", event.id, duration)
	fmt.Fprintln(event.log, message)

	// Log to the go test log as well
	event.world.t.Helper()
	event.world.t.Log(message)

	event.log.Close()
}
