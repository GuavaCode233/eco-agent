package platform

import (
	"errors"
	"testing"
)

func TestSingleInstance(t *testing.T) {
	release, err := AcquireSingleInstance()
	if errors.Is(err, ErrAlreadyRunning) {
		t.Skip("an Eco-Agent instance is running on this machine")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireSingleInstance(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire = %v, want ErrAlreadyRunning", err)
	}
	release()
	release2, err := AcquireSingleInstance()
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}
