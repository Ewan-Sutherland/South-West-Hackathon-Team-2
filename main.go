package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"

	"github.com/becomeliminal/nim-go-sdk/server"
	"github.com/becomeliminal/nim-go-sdk/tools"
)

func main() {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		log.Fatal("ANTHROPIC_API_KEY not set")
	}

	srv, err := server.New(server.Config{AnthropicKey: key})
	if err != nil {
		log.Fatal(err)
	}

	srv.AddTool(tools.New("cio_allocate").
		Description("Allocate across Directional/Yield/Stable/Cash using transaction history. Runs a Python allocator.").
		Schema(tools.ObjectSchema(map[string]interface{}{
			"risk_level":  tools.StringProperty("low | medium | high"),
			"regime_hint": tools.StringProperty("Optional: bull | bear | sideways | vol_spike"),
			"transactions": tools.ArrayProperty(
				"Transaction history as JSON array",
				tools.ObjectSchema(map[string]interface{}{
					"timestamp": tools.StringProperty("ISO-8601 timestamp"),
					"merchant":  tools.StringProperty("Merchant/counterparty"),
					"amount":    tools.NumberProperty("Signed amount (negative=spend)"),
					"currency":  tools.StringProperty("Currency code, e.g. GBP"),
				}, "timestamp", "merchant", "amount", "currency"),
			),
		}, "risk_level", "transactions")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			// Call Python, pass input JSON via stdin, get JSON back on stdout.
			cmd := exec.Command("python", "python/allocator.py")
			cmd.Stdin = bytesReader(input)

			out, err := cmd.Output()
			if err != nil {
				// include stderr if you want: cmd.CombinedOutput()
				return nil, err
			}

			// Python returns JSON. Decode to interface{} and return.
			var result interface{}
			if err := json.Unmarshal(out, &result); err != nil {
				return nil, err
			}
			return result, nil
		}).
		Build())

	log.Println("Nim server on :8080")
	srv.Run(":8080")
}

// tiny helper to avoid importing bytes in snippet explanation
func bytesReader(b []byte) *os.File {
	// In your real code: use bytes.NewReader(b)
	// Keeping snippet short: replace this with bytes.NewReader in actual file.
	return os.Stdin
}
