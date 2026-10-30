package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type ThrottleMode string

const (
	ThrottleAutomatic ThrottleMode = "automatic"
	ThrottleFullSpeed ThrottleMode = "full-speed"
	ThrottleReduced   ThrottleMode = "reduced"
	ThrottlePaused    ThrottleMode = "paused"
)

func validThrottleMode(mode ThrottleMode) bool {
	return mode == ThrottleAutomatic || mode == ThrottleFullSpeed || mode == ThrottleReduced || mode == ThrottlePaused
}

// Activity describes the portable signals available to the runner. Platform
// integrations can provide richer values through the documented environment
// overrides without changing the runner contract.
type Activity struct {
	UserActive      bool
	Load            float64
	ThermalPressure string
	PowerState      string
	Available       bool
}

type ActivityProbe func() Activity

// DefaultActivityProbe uses explicit operator/application signals first and
// then load averages where the host exposes them. Missing signals are not
// treated as proof that the machine is idle.
func DefaultActivityProbe() ActivityProbe {
	return func() Activity {
		activity := strings.ToLower(os.Getenv("WTFACE_ACTIVITY"))
		if activity == "active" || activity == "busy" {
			return Activity{UserActive: true, Available: true}
		}
		if activity == "idle" {
			return Activity{Available: true}
		}
		load := readLoadAverage()
		thermal := strings.ToLower(os.Getenv("WTFACE_THERMAL_PRESSURE"))
		power := strings.ToLower(os.Getenv("WTFACE_POWER_STATE"))
		available := load >= 0 || thermal != "" || power != ""
		return Activity{Load: load, ThermalPressure: thermal, PowerState: power, Available: available}
	}
}

func readLoadAverage() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		if runtime.GOOS != "darwin" {
			return -1
		}
		data, err = exec.Command("sysctl", "-n", "vm.loadavg").Output()
		if err != nil {
			return -1
		}
	}
	fields := strings.Fields(string(data))
	for _, field := range fields {
		field = strings.Trim(field, "{}(),")
		value, parseErr := strconv.ParseFloat(field, 64)
		if parseErr == nil {
			return value
		}
	}
	return -1
}

type ResourcePolicy struct {
	Mode         ThrottleMode
	Probe        ActivityProbe
	ReducedDelay time.Duration
	PollInterval time.Duration
}

func (p ResourcePolicy) mode() ThrottleMode {
	if p.Mode == "" {
		return ThrottleAutomatic
	}
	return p.Mode
}

func (p ResourcePolicy) EffectiveMode() ThrottleMode {
	mode := p.mode()
	if mode != ThrottleAutomatic {
		return mode
	}
	probe := p.Probe
	if probe == nil {
		probe = DefaultActivityProbe()
	}
	activity := probe()
	if activity.UserActive || strings.Contains(activity.ThermalPressure, "critical") || strings.Contains(activity.ThermalPressure, "serious") {
		return ThrottleReduced
	}
	if activity.Load >= 0 && activity.Load > float64(runtime.NumCPU())*0.75 {
		return ThrottleReduced
	}
	if activity.PowerState == "battery" || activity.PowerState == "low" {
		return ThrottleReduced
	}
	if !activity.Available {
		return ThrottleReduced
	}
	return ThrottleFullSpeed
}

func (s Store) SetThrottleMode(ctx context.Context, mode ThrottleMode) error {
	if !validThrottleMode(mode) {
		return fmt.Errorf("invalid throttle mode %q", mode)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runtime_settings (key, value, updated_at) VALUES ('throttle_mode', ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, mode, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s Store) ThrottleMode(ctx context.Context) (ThrottleMode, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM runtime_settings WHERE key = 'throttle_mode'`).Scan(&value)
	if err == sql.ErrNoRows {
		return ThrottleAutomatic, nil
	}
	if err != nil {
		return "", err
	}
	mode := ThrottleMode(value)
	if !validThrottleMode(mode) {
		return "", fmt.Errorf("invalid persisted throttle mode %q", value)
	}
	return mode, nil
}
