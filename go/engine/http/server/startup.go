// Linux only, because it serves a Linux-only engine.
//go:build linux

// The startup sequence, as a checked list rather than a function that happens
// to do things in an order.
//
// One ordering rule makes the sandbox mean anything: it is applied before any
// long-lived state is opened or any token is minted. A descriptor opened
// before the sandbox stays usable after it, so a step that runs early gives
// away exactly what the confinement was meant to withhold.
package server

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// StartupStep names one step of the sequence.
type StartupStep uint8

const (
	// StepUnsetStartup is the zero value and never appears in a sequence.
	StepUnsetStartup StartupStep = iota

	// StepParseArgs reads the command and the data directory.
	StepParseArgs

	// StepRequireResolver refuses a kernel without the race-free path
	// resolver. Everything below assumes a resolve cannot be raced.
	StepRequireResolver

	// StepDeriveHardening pre-opens just enough state to learn the hardening
	// policy and the share host parents, then closes it. Deliberately before
	// the sandbox and deliberately short-lived: the sandbox needs to know what
	// to grant, and nothing may keep a descriptor from this step.
	StepDeriveHardening

	// StepApplySandbox applies the confinement, re-executing if that is what
	// the platform needs.
	StepApplySandbox

	// StepLockDataDir takes the exclusive lock, so two processes cannot serve
	// one data directory.
	StepLockDataDir

	// StepOpenServices opens the store, the master key, the ACL evaluator, the
	// core and every service, in dependency order.
	StepOpenServices

	// StepBuildPresentation constructs setup, the handlers and the chain.
	StepBuildPresentation

	// StepStartTasks starts background work.
	StepStartTasks

	// StepServe binds and accepts.
	StepServe
)

// String is the step's name in a diagnostic.
func (s StartupStep) String() string {
	switch s {
	case StepParseArgs:
		return "ParseArgs"
	case StepRequireResolver:
		return "RequireResolver"
	case StepDeriveHardening:
		return "DeriveHardening"
	case StepApplySandbox:
		return "ApplySandbox"
	case StepLockDataDir:
		return "LockDataDir"
	case StepOpenServices:
		return "OpenServices"
	case StepBuildPresentation:
		return "BuildPresentation"
	case StepStartTasks:
		return "StartTasks"
	case StepServe:
		return "Serve"
	case StepUnsetStartup:
		return "unset"
	default:
		return fmt.Sprintf("StartupStep(%d)", uint8(s))
	}
}

// StartupSequence is the order the process starts in.
func StartupSequence() []StartupStep {
	return []StartupStep{
		StepParseArgs,
		StepRequireResolver,
		StepDeriveHardening,
		StepApplySandbox,
		StepLockDataDir,
		StepOpenServices,
		StepBuildPresentation,
		StepStartTasks,
		StepServe,
	}
}

// afterSandbox are the steps that must not run before the confinement.
//
// Each opens something that outlives the step. A descriptor obtained before
// the sandbox keeps working after it, so running any of these early hands out
// precisely what the sandbox exists to withhold.
func afterSandbox() []StartupStep {
	return []StartupStep{
		StepLockDataDir,
		StepOpenServices,
		StepBuildPresentation,
		StepStartTasks,
		StepServe,
	}
}

// ValidateStartup reports every way a sequence is not the sequence.
func ValidateStartup(steps []StartupStep) error {
	var problems []string

	if len(steps) == 0 {
		return fmt.Errorf("startup order: the sequence is empty")
	}

	at := map[StartupStep]int{}
	for i, s := range steps {
		switch {
		case s == StepUnsetStartup:
			problems = append(problems, fmt.Sprintf("position %d does not name a step", i))
			continue
		case s > StepServe:
			problems = append(problems, fmt.Sprintf("position %d names %s, which is not a step", i, s))
			continue
		}
		if _, dup := at[s]; dup {
			problems = append(problems, fmt.Sprintf("%s appears more than once", s))
		}
		at[s] = i
	}

	for _, s := range StartupSequence() {
		if _, ok := at[s]; !ok {
			problems = append(problems, fmt.Sprintf("%s is missing", s))
		}
	}

	sandbox, hasSandbox := at[StepApplySandbox]
	if !hasSandbox {
		problems = append(problems, "ApplySandbox is missing, so nothing is confined")
	} else {
		for _, s := range afterSandbox() {
			if i, ok := at[s]; ok && i < sandbox {
				problems = append(problems, fmt.Sprintf(
					"%s runs before ApplySandbox, so what it opens escapes the sandbox", s))
			}
		}
		// The hardening read is the one thing that must precede the sandbox:
		// the sandbox cannot grant paths it has not read yet.
		if i, ok := at[StepDeriveHardening]; ok && i > sandbox {
			problems = append(problems,
				"DeriveHardening runs after ApplySandbox, which cannot grant paths it has not read")
		}
	}

	// The resolver refusal is first among the checks because everything below
	// assumes a resolve cannot be raced.
	if r, ok := at[StepRequireResolver]; ok {
		for _, s := range []StartupStep{StepDeriveHardening, StepOpenServices} {
			if i, iok := at[s]; iok && i < r {
				problems = append(problems, fmt.Sprintf(
					"%s runs before RequireResolver, so it resolves paths on a kernel that may race", s))
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("startup order: %s", strings.Join(slices.Compact(problems), "; "))
}
