package cli

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
)

func gatewayKeyConcurrency(out io.Writer, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: magpie gateway-key concurrency <id> [off | --max N [--queue N] [--wait seconds]]")
	}
	id := args[0]
	keys, err := access.List()
	if err != nil {
		return err
	}
	var key *access.Key
	for i := range keys {
		if keys[i].ID == id {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return fmt.Errorf("Key not found")
	}
	if len(args) == 1 {
		if key.Concurrency == nil {
			_, err = fmt.Fprintf(out, "%s: concurrency unlimited\n", key.Name)
		} else {
			p := key.Concurrency
			_, err = fmt.Fprintf(out, "%s: %d active, %d queued, %ds wait\n", key.Name, p.MaxConcurrency, p.QueueLimit, p.QueueWait)
		}
		return err
	}
	var p *access.Concurrency
	if !(len(args) == 2 && args[1] == "off") {
		p = &access.Concurrency{}
		seen := map[string]bool{}
		for i := 1; i < len(args); i += 2 {
			if i+1 >= len(args) || seen[args[i]] {
				return fmt.Errorf("concurrency options need one numeric value each")
			}
			seen[args[i]] = true
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("invalid concurrency number %q", args[i+1])
			}
			switch args[i] {
			case "--max":
				p.MaxConcurrency = n
			case "--queue":
				p.QueueLimit = n
			case "--wait":
				p.QueueWait = n
			default:
				return fmt.Errorf("unknown concurrency option %q", args[i])
			}
		}
		if !seen["--max"] {
			return fmt.Errorf("--max is required when setting concurrency")
		}
	}
	if _, err := access.Update("concurrency-key", access.Change{Key: id, Concurrency: p}); err != nil {
		return err
	}
	return gatewayKeyConcurrency(out, []string{id})
}

func gatewayAccounting(out io.Writer, base string, args []string) error {
	method, path := http.MethodGet, "/v1/magpie/accounting"
	if len(args) > 0 {
		if len(args) != 1 || args[0] != "retry" {
			return fmt.Errorf("usage: magpie gateway-key accounting [retry]")
		}
		method, path = http.MethodPost, path+"/retry"
	}
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+gateway.Token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err = io.Copy(out, io.LimitReader(resp.Body, 64<<10)); err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("accounting endpoint returned %s", resp.Status)
	}
	return nil
}
