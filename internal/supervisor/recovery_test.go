package supervisor_test

import (
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/supervisor"
)

func TestRecoveryClassifier_QuotaFailover(t *testing.T) {
	c := supervisor.NewRecoveryClassifier()
	d := c.Classify(supervisor.RecoveryInput{
		Failure:            protocol.DefaultPolicy(protocol.FailureProviderQuota),
		FallbacksAvailable: true,
		AnyRouteAvailable:  true,
	})
	if d.Action != supervisor.ActionFailover {
		t.Fatalf("quota with fallback: want failover, got %s", d.Action)
	}
}

func TestRecoveryClassifier_QuotaNoFallback_WaitQuota(t *testing.T) {
	c := supervisor.NewRecoveryClassifier()
	d := c.Classify(supervisor.RecoveryInput{
		Failure:            protocol.DefaultPolicy(protocol.FailureProviderQuota),
		FallbacksAvailable: false,
		AnyRouteAvailable:  false, // all routes exhausted
	})
	if d.Action != supervisor.ActionWaitQuota {
		t.Fatalf("quota no fallback all-exhausted: want wait_quota, got %s", d.Action)
	}
}

func TestRecoveryClassifier_AuthNoFallback_Quarantine(t *testing.T) {
	c := supervisor.NewRecoveryClassifier()
	d := c.Classify(supervisor.RecoveryInput{
		Failure:            protocol.DefaultPolicy(protocol.FailureProviderAuth),
		FallbacksAvailable: false,
		AnyRouteAvailable:  true,
	})
	// Auth is not quota-waitable -> quarantine.
	if d.Action != supervisor.ActionQuarantine {
		t.Fatalf("auth no fallback: want quarantine, got %s", d.Action)
	}
}

func TestRecoveryClassifier_RateLimitBoundedRetry(t *testing.T) {
	c := &supervisor.RecoveryClassifier{Jitter: 0, Now: time.Now, Rand: func() float64 { return 0 }}
	pol := protocol.DefaultPolicy(protocol.FailureProviderRateLimit)
	// First two retries within budget.
	for used := 0; used < pol.MaxRetries; used++ {
		d := c.Classify(supervisor.RecoveryInput{
			Failure:            pol,
			AttemptsUsed:       used,
			FallbacksAvailable: true,
			AnyRouteAvailable:  true,
		})
		if d.Action != supervisor.ActionRetry {
			t.Fatalf("attempt %d: want retry, got %s", used, d.Action)
		}
		if d.Cooldown <= 0 {
			t.Fatalf("attempt %d: cooldown should be positive, got %v", used, d.Cooldown)
		}
		if d.AttemptsMax != pol.MaxRetries {
			t.Fatalf("attempts max = %d, want %d", d.AttemptsMax, pol.MaxRetries)
		}
	}
	// Budget exhausted -> failover.
	d := c.Classify(supervisor.RecoveryInput{
		Failure:            pol,
		AttemptsUsed:       pol.MaxRetries,
		FallbacksAvailable: true,
		AnyRouteAvailable:  true,
	})
	if d.Action != supervisor.ActionFailover {
		t.Fatalf("exhausted: want failover, got %s", d.Action)
	}
}

func TestRecoveryClassifier_Terminal(t *testing.T) {
	c := supervisor.NewRecoveryClassifier()
	for _, class := range []protocol.FailureClass{
		protocol.FailureBuildFailure, protocol.FailureTestFailure,
		protocol.FailureScopeViolation, protocol.FailurePolicyViolation,
	} {
		d := c.Classify(supervisor.RecoveryInput{
			Failure: protocol.DefaultPolicy(class),
		})
		if d.Action != supervisor.ActionTerminal {
			t.Fatalf("%s: want terminal, got %s", class, d.Action)
		}
	}
}

func TestRecoveryClassifier_ProtocolErrorQuarantines(t *testing.T) {
	c := &supervisor.RecoveryClassifier{Jitter: 0, Now: time.Now, Rand: func() float64 { return 0 }}
	pol := protocol.DefaultPolicy(protocol.FailureEngineProtocol)
	// One bounded retry, then quarantine.
	d0 := c.Classify(supervisor.RecoveryInput{Failure: pol, AttemptsUsed: 0, FallbacksAvailable: false})
	if d0.Action != supervisor.ActionRetry {
		t.Fatalf("protocol error attempt 0: want retry, got %s", d0.Action)
	}
	d1 := c.Classify(supervisor.RecoveryInput{Failure: pol, AttemptsUsed: pol.MaxRetries, FallbacksAvailable: false})
	if d1.Action != supervisor.ActionQuarantine {
		t.Fatalf("protocol error exhausted: want quarantine, got %s", d1.Action)
	}
}

func TestRecoveryClassifier_NoInfiniteRetry(t *testing.T) {
	// Property: no action returned is retry when the budget is already
	// exhausted and no fallback exists. Ensures bounded retry (spec §32).
	c := supervisor.NewRecoveryClassifier()
	for _, class := range []protocol.FailureClass{
		protocol.FailureProviderRateLimit, protocol.FailureEngineCrash,
		protocol.FailureTimeout, protocol.FailureMalformedOutput,
	} {
		pol := protocol.DefaultPolicy(class)
		d := c.Classify(supervisor.RecoveryInput{
			Failure:            pol,
			AttemptsUsed:       pol.MaxRetries + 10,
			FallbacksAvailable: false,
			AnyRouteAvailable:  false,
		})
		if d.Action == supervisor.ActionRetry {
			t.Fatalf("%s: action must not be retry when budget exhausted and no fallback", class)
		}
	}
}

func TestRecoveryClassifier_JitterAddsVariance(t *testing.T) {
	c := &supervisor.RecoveryClassifier{Jitter: 0.5, Now: time.Now, Rand: func() float64 { return 1.0 }}
	pol := protocol.DefaultPolicy(protocol.FailureProviderRateLimit)
	d := c.Classify(supervisor.RecoveryInput{Failure: pol, AttemptsUsed: 0, FallbacksAvailable: true, AnyRouteAvailable: true})
	// Rand=1.0 + Jitter=0.5 + base 30s => 30 + 15 = 45s.
	if d.Cooldown != 45*time.Second {
		t.Fatalf("jitter cooldown: want 45s, got %v", d.Cooldown)
	}
}
