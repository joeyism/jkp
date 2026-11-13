package ergoacct

import (
	"errors"
	"strings"
	"testing"
)

func TestExistingErgoAccountGetsPasswordUpdated(t *testing.T) {
	updated := false
	err := ensureAccount("host", func() error {
		return errAccountExists
	}, func() error {
		updated = true
		return nil
	}, func() error { t.Fatal("reclaim"); return nil })
	if err != nil || !updated {
		t.Fatalf("ensureAccount err=%v updated=%v", err, updated)
	}
}

func TestNewErgoAccountDoesNotNeedPasswordUpdate(t *testing.T) {
	updated := false
	err := ensureAccount("guest", func() error { return nil }, func() error {
		updated = true
		return nil
	}, func() error { t.Fatal("reclaim"); return nil })
	if err != nil || updated {
		t.Fatalf("ensureAccount err=%v updated=%v", err, updated)
	}
}

func TestAccountUpdateFailureIsReturned(t *testing.T) {
	want := errors.New("passwd failed")
	err := ensureAccount("guest", func() error { return errAccountExists }, func() error { return want }, func() error { t.Fatal("reclaim"); return nil })
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want wrapped %v", err, want)
	}
}

func TestReservedErgoAccountIsReclaimedThenRegistered(t *testing.T) {
	var steps []string
	err := ensureAccount("host",
		func() error {
			steps = append(steps, "register")
			if len(steps) == 1 {
				return errAccountReserved
			}
			return nil
		},
		func() error {
			steps = append(steps, "passwd")
			return nil
		},
		func() error {
			steps = append(steps, "reclaim")
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(steps, ",") != "register,reclaim,register" {
		t.Fatalf("steps = %v", steps)
	}
}

func TestEraseConfirmCodeParsesNickServNotice(t *testing.T) {
	got := eraseConfirmCode("To confirm, run this command: /NS ERASE host abc123def", "host")
	if got != "abc123def" {
		t.Fatalf("code = %q", got)
	}
}
