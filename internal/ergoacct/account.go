// Package ergoacct creates and updates SASL accounts on an Ergo server using
// an operator connection.
package ergoacct

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

var (
	errAccountExists   = errors.New("Ergo account already exists")
	errAccountReserved = errors.New("Ergo account name reserved")
)

// Registrar performs NickServ account operations over an IRC operator
// connection.
type Registrar struct {
	Addr     string
	OperUser string
	OperPass string
}

// Register creates an account, updates the password of an existing account,
// or reclaims a reserved name before creating it.
func (r Registrar) Register(nick, pass string) error {
	return r.withOperator(func(send func(string), readUntilAny func(...string) string) error {
		return ensureAccount(nick, func() error {
			send(fmt.Sprintf("NICKSERV SAREGISTER %s %s", nick, pass))
			response := readUntilAny("Successfully registered account "+nick, "Account already exists", "Name reserved due to a prior registration")
			if strings.Contains(response, "Successfully registered account "+nick) {
				return nil
			}
			if strings.Contains(response, "Account already exists") {
				return errAccountExists
			}
			if strings.Contains(response, "Name reserved due to a prior registration") {
				return errAccountReserved
			}
			return fmt.Errorf("SAREGISTER %s failed: %s", nick, strings.TrimSpace(response))
		}, func() error {
			send(fmt.Sprintf("NICKSERV PASSWD %s %s", nick, pass))
			response := readUntilAny("Password changed")
			if !strings.Contains(response, "Password changed") {
				return fmt.Errorf("PASSWD %s failed: %s", nick, strings.TrimSpace(response))
			}
			return nil
		}, func() error {
			send(fmt.Sprintf("NICKSERV ERASE %s", nick))
			response := readUntilAny("To confirm", "Successfully unregistered account "+nick)
			if !strings.Contains(response, "Successfully unregistered account "+nick) {
				code := eraseConfirmCode(response, nick)
				if code == "" {
					return fmt.Errorf("ERASE %s failed: %s", nick, strings.TrimSpace(response))
				}
				send(fmt.Sprintf("NICKSERV ERASE %s %s", nick, code))
				response = readUntilAny("Successfully unregistered account " + nick)
				if !strings.Contains(response, "Successfully unregistered account "+nick) {
					return fmt.Errorf("ERASE %s confirm failed: %s", nick, strings.TrimSpace(response))
				}
			}
			return nil
		})
	})
}

// RegisterNew only creates a fresh account. It never changes or erases an
// account that already exists; callers can safely retry with a new random nick.
func (r Registrar) RegisterNew(nick, pass string) error {
	return r.withOperator(func(send func(string), readUntilAny func(...string) string) error {
		send(fmt.Sprintf("NICKSERV SAREGISTER %s %s", nick, pass))
		response := readUntilAny("Successfully registered account "+nick, "Account already exists", "Name reserved due to a prior registration")
		switch {
		case strings.Contains(response, "Successfully registered account "+nick):
			return nil
		case strings.Contains(response, "Account already exists"):
			return errAccountExists
		case strings.Contains(response, "Name reserved due to a prior registration"):
			return errAccountReserved
		default:
			return fmt.Errorf("SAREGISTER %s failed: %s", nick, strings.TrimSpace(response))
		}
	})
}

func (r Registrar) withOperator(operation func(send func(string), readUntilAny func(...string) string) error) error {
	addr := r.Addr
	if addr == "" {
		addr = "127.0.0.1:6667"
	}
	operUser, operPass := r.OperUser, r.OperPass
	if operUser == "" {
		operUser = "admin"
	}
	if operPass == "" {
		operPass = "jkp-test-oper-pw"
	}
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	send := func(line string) { _, _ = fmt.Fprintf(conn, "%s\r\n", line) }
	readUntilAny := func(markers ...string) string {
		buf := make([]byte, 4096)
		var all strings.Builder
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			n, err := conn.Read(buf)
			if err != nil {
				break
			}
			all.Write(buf[:n])
			text := all.String()
			for _, marker := range markers {
				if strings.Contains(text, marker) {
					return text
				}
			}
		}
		return all.String()
	}
	send("NICK jkpsetup")
	send("USER jkpsetup 0 * :jkp setup")
	readUntilAny(" 001 ")
	send("OPER " + operUser + " " + operPass)
	if response := readUntilAny(" 381 "); !strings.Contains(response, " 381 ") {
		return fmt.Errorf("Ergo OPER failed: %s", strings.TrimSpace(response))
	}
	err = operation(send, readUntilAny)
	send("QUIT :done")
	return err
}

func ensureAccount(nick string, register, updatePassword, reclaim func() error) error {
	if err := register(); err == nil {
		return nil
	} else if errors.Is(err, errAccountExists) {
		if err := updatePassword(); err != nil {
			return fmt.Errorf("update password for existing Ergo account %s: %w", nick, err)
		}
		return nil
	} else if errors.Is(err, errAccountReserved) {
		if err := reclaim(); err != nil {
			return fmt.Errorf("reclaim reserved Ergo account %s: %w", nick, err)
		}
		if err := register(); err != nil {
			return fmt.Errorf("reregister Ergo account %s: %w", nick, err)
		}
		return nil
	} else {
		return err
	}
}

func eraseConfirmCode(response, nick string) string {
	marker := "/NS ERASE " + nick + " "
	idx := strings.LastIndex(response, marker)
	if idx < 0 {
		marker = "NICKSERV ERASE " + nick + " "
		idx = strings.LastIndex(response, marker)
	}
	if idx < 0 {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(response[idx+len(marker):]))
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimRight(fields[0], ".")
}
