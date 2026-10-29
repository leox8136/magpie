package cli

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

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
