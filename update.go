//go:build ignore

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func runStep(title string, cmdName string, args ...string) error {
	fmt.Printf("\n▶ %s...\n", title)
	cmd := exec.Command(cmdName, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func main() {
	start := time.Now()
	fmt.Println("==================================================")
	fmt.Println("       Bebot Dependency & Data Updater           ")
	fmt.Println("==================================================")

	// Step 1: Update Go module dependencies
	fmt.Println("\n[1/4] Checking and updating Go dependencies...")
	directDeps := []string{
		"github.com/sandertv/gophertunnel@latest",
		"github.com/sandertv/go-raknet@latest",
		"github.com/df-mc/go-xsapi/v2@latest",
		"github.com/google/uuid@latest",
		"github.com/go-gl/mathgl@latest",
	}
	for _, dep := range directDeps {
		if err := runStep("Updating "+dep, "go", "get", dep); err != nil {
			fmt.Printf("Warning: failed to update %s: %v\n", dep, err)
		}
	}

	if err := runStep("Running 'go mod tidy'", "go", "mod", "tidy"); err != nil {
		fmt.Printf("Error running 'go mod tidy': %v\n", err)
		os.Exit(1)
	}

	// Step 2: Patch gophertunnel if needed
	fmt.Println("\n[2/4] Checking and patching gophertunnel...")
	if err := runStep("Running helper/patch_gophertunnel.go", "go", "run", "helper/patch_gophertunnel.go"); err != nil {
		fmt.Printf("Error running patch_gophertunnel.go: %v\n", err)
		os.Exit(1)
	}

	// Step 3: Check and update block palette if outdated
	fmt.Println("\n[3/4] Checking Bedrock block palette data...")
	if err := runStep("Running helper/download_palette.go --if-outdated", "go", "run", "helper/download_palette.go", "--if-outdated"); err != nil {
		fmt.Printf("Error running download_palette.go: %v\n", err)
		os.Exit(1)
	}

	// Step 4: Verify build and unit tests
	fmt.Println("\n[4/4] Verifying build and tests...")
	if err := runStep("Running unit tests", "go", "test", "./src/bot/..."); err != nil {
		fmt.Printf("Unit tests failed after update: %v\n", err)
		os.Exit(1)
	}

	if err := runStep("Compiling Bebot binary", "go", "build", "."); err != nil {
		fmt.Printf("Build failed after update: %v\n", err)
		os.Exit(1)
	}

	// Read updated gophertunnel version for display
	gtVer := "unknown"
	if out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/sandertv/gophertunnel").Output(); err == nil {
		gtVer = strings.TrimSpace(string(out))
	}

	fmt.Println("\n==================================================")
	fmt.Println(" ✔ Update Complete!")
	fmt.Printf("   • gophertunnel: %s (patched to ChunkRadius: 4)\n", gtVer)
	fmt.Printf("   • Block Palette: Checked & up to date in data/\n")
	fmt.Printf("   • Tests: Passed\n")
	fmt.Printf("   • Binary: bebot compiled successfully\n")
	fmt.Printf("   • Total time: %.2fs\n", time.Since(start).Seconds())
	fmt.Println("==================================================")
}
