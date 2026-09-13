package monitor

import (
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// MonitorState is the latest snapshot of monitoring data.
type MonitorState struct {
	Profiles  map[string]*ProfileState
	UpdatedAt time.Time
	Errors    []string
}

// ProfileState represents monitoring data for a single profile.
type ProfileState struct {
	Provider      string
	ProfileName   string
	Usage         *usage.UsageInfo
	Health        health.HealthStatus
	PoolStatus    authpool.PoolStatus
	InCooldown    bool
	CooldownUntil *time.Time
	Alert         *Alert
	// Active reports that this is the profile its provider's tool is using
	// right now, when the monitor was given a way to tell.
	Active bool
}

// Clone returns a shallow copy of the state and profile map for safe reads.
func (s *MonitorState) Clone() *MonitorState {
	if s == nil {
		return nil
	}

	clone := &MonitorState{
		UpdatedAt: s.UpdatedAt,
	}

	if len(s.Errors) > 0 {
		clone.Errors = append([]string(nil), s.Errors...)
	}

	if len(s.Profiles) > 0 {
		clone.Profiles = make(map[string]*ProfileState, len(s.Profiles))
		for key, profile := range s.Profiles {
			if profile == nil {
				continue
			}
			cp := *profile
			clone.Profiles[key] = &cp
		}
	}

	return clone
}

func profileKey(provider, name string) string {
	return provider + "/" + name
}
