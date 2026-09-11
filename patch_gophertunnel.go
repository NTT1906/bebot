//go:build ignore

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	fmt.Println("Finding GOPATH...")
	out, err := exec.Command("go", "env", "GOPATH").Output()
	if err != nil {
		fmt.Printf("Error finding GOPATH: %v\n", err)
		os.Exit(1)
	}

	gopath := strings.TrimSpace(string(out))
	if gopath == "" {
		fmt.Println("GOPATH is empty.")
		os.Exit(1)
	}

	connGoPath := filepath.Join(gopath, "pkg", "mod", "github.com", "sandertv", "gophertunnel@v1.61.0", "minecraft", "conn.go")
	fmt.Printf("Target file: %s\n", connGoPath)

	content, err := os.ReadFile(connGoPath)
	if err != nil {
		fmt.Printf("Error reading file (is gophertunnel@v1.61.0 downloaded?): %v\n", err)
		os.Exit(1)
	}

	oldStr := []byte("ChunkRadius: 16, MaxChunkRadius: 16")
	newStr := []byte("ChunkRadius: 4, MaxChunkRadius: 4")

	if !bytes.Contains(content, oldStr) {
		if bytes.Contains(content, newStr) {
			fmt.Println("The file is already patched! No changes needed.")
			return
		}
		fmt.Println("Could not find the target string 'ChunkRadius: 16, MaxChunkRadius: 16' in conn.go.")
		fmt.Println("The file might have been modified by another update.")
		os.Exit(1)
	}

	fmt.Println("Patching file...")
	patchedContent := bytes.Replace(content, oldStr, newStr, 1)

	// Make the file writable
	info, err := os.Stat(connGoPath)
	if err != nil {
		fmt.Printf("Error stating file: %v\n", err)
		os.Exit(1)
	}

	err = os.Chmod(connGoPath, 0644)
	if err != nil {
		fmt.Printf("Warning: failed to chmod file (might already be writable or lack permissions): %v\n", err)
	}

	err = os.WriteFile(connGoPath, patchedContent, info.Mode())
	if err != nil {
		fmt.Printf("Error writing patched file: %v\n", err)
		os.Exit(1)
	}

	// Restore read-only permissions typical of module cache
	_ = os.Chmod(connGoPath, 0444)

	fmt.Println("Successfully patched gophertunnel! The bot will now request 4 chunks during login.")
}
