//go:build ignore

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/nbt"
)

type Mirror struct {
	Name     string
	URL      string
	IsJSON   bool
	Filename string
}

var mirrors = []Mirror{
	{
		Name:     "CloudburstMC Data (NBT)",
		URL:      "https://raw.githubusercontent.com/CloudburstMC/Data/master/block_palette.nbt",
		Filename: "block_palette.nbt",
	},
	{
		Name:     "GeyserMC Core (NBT)",
		URL:      "https://raw.githubusercontent.com/GeyserMC/Geyser/master/core/src/main/resources/bedrock/block_palette.nbt",
		Filename: "block_palette.nbt",
	},
	{
		Name:     "Altay BedrockData (NBT)",
		URL:      "https://raw.githubusercontent.com/altayofficial/BedrockData/master/block_palette.nbt",
		Filename: "block_palette.nbt",
	},
	{
		Name:     "Bedrock Crustaceans (JSON)",
		URL:      "https://raw.githubusercontent.com/bedrock-crustaceans/vanilla/main/data/blocks/block_hashes.json",
		IsJSON:   true,
		Filename: "block_hashes.json",
	},
}

const (
	dataDir  = "data"
	metaPath = "data/block_palette_meta.json"
)

type PaletteMetadata struct {
	SourceMirror   string    `json:"source_mirror"`
	SourceURL      string    `json:"source_url"`
	DownloadedAt   time.Time `json:"downloaded_at"`
	TargetFile     string    `json:"target_file"`
	BedrockVersion string    `json:"bedrock_version"`
	BlockCount     int       `json:"block_count"`
	FileSizeBytes  int64     `json:"file_size_bytes"`
	SHA256         string    `json:"sha256"`
}

func main() {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		fmt.Printf("Error creating directory %s: %v\n", dataDir, err)
		os.Exit(1)
	}

	checkOnly := false
	for _, arg := range os.Args[1:] {
		if arg == "--if-outdated" || arg == "-u" || arg == "--check" {
			checkOnly = true
		}
	}

	var existingMeta *PaletteMetadata
	if metaBytes, err := os.ReadFile(metaPath); err == nil {
		var em PaletteMetadata
		if err := json.Unmarshal(metaBytes, &em); err == nil {
			existingMeta = &em
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	var downloadedData []byte
	var successfulMirror *Mirror

	fmt.Println("Attempting to download Bedrock block palette from available mirrors...")
	for i, m := range mirrors {
		fmt.Printf(" [%d/%d] Trying %s...\n       %s\n", i+1, len(mirrors), m.Name, m.URL)
		req, err := http.NewRequest(http.MethodGet, m.URL, nil)
		if err != nil {
			fmt.Printf("       Request error: %v\n", err)
			continue
		}
		req.Header.Set("User-Agent", "Bebot-Palette-Downloader/1.0")

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("       Connection error: %v\n", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			fmt.Printf("       HTTP failure: status %s\n", resp.Status)
			resp.Body.Close()
			continue
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			fmt.Printf("       Read error: %v\n", err)
			continue
		}

		if len(data) < 1000 {
			fmt.Printf("       Downloaded data too small (%d bytes), likely error page.\n", len(data))
			continue
		}

		downloadedData = data
		successfulMirror = &m
		fmt.Printf("       -> Success! Downloaded %d bytes (~%.1f KB)\n", len(data), float64(len(data))/1024.0)
		break
	}

	if downloadedData == nil || successfulMirror == nil {
		fmt.Println("All mirrors failed. Please check internet connection or repository URLs.")
		os.Exit(1)
	}

	targetFilePath := filepath.Join(dataDir, successfulMirror.Filename)

	// Calculate SHA256 checksum
	hasher := sha256.New()
	hasher.Write(downloadedData)
	checksum := hex.EncodeToString(hasher.Sum(nil))

	// Check if already up-to-date
	if checkOnly && existingMeta != nil {
		if _, err := os.Stat(targetFilePath); err == nil && existingMeta.SHA256 == checksum {
			fmt.Printf("\nBlock palette is already up to date (%s, Bedrock v%s, %d states).\nNo update required.\n",
				successfulMirror.Filename, existingMeta.BedrockVersion, existingMeta.BlockCount)
			return
		}
	}

	// Inspect content to extract Bedrock version and block count
	var versionStr string
	var blockCount int
	var inspectErr error

	if successfulMirror.IsJSON {
		versionStr, blockCount, inspectErr = inspectJSONPalette(downloadedData)
	} else {
		versionStr, blockCount, inspectErr = inspectNBTPalette(downloadedData)
	}

	if inspectErr != nil {
		fmt.Printf("Warning: could not inspect palette contents: %v\n", inspectErr)
		if versionStr == "" {
			versionStr = "unknown"
		}
	}

	// Write file into data/
	if err := os.WriteFile(targetFilePath, downloadedData, 0644); err != nil {
		fmt.Printf("Error saving file %s: %v\n", targetFilePath, err)
		os.Exit(1)
	}

	// Write metadata JSON
	meta := PaletteMetadata{
		SourceMirror:   successfulMirror.Name,
		SourceURL:      successfulMirror.URL,
		DownloadedAt:   time.Now().UTC(),
		TargetFile:     successfulMirror.Filename,
		BedrockVersion: versionStr,
		BlockCount:     blockCount,
		FileSizeBytes:  int64(len(downloadedData)),
		SHA256:         checksum,
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(metaPath, metaJSON, 0644)

	fmt.Println("\n=== Block Palette Download Complete ===")
	fmt.Printf(" Saved To:        %s\n", targetFilePath)
	fmt.Printf(" Metadata:        %s\n", metaPath)
	fmt.Printf(" Source Mirror:   %s\n", successfulMirror.Name)
	fmt.Printf(" Bedrock Version: %s\n", versionStr)
	fmt.Printf(" Block States:    %d entries\n", blockCount)
	fmt.Printf(" File Size:       %.1f KB\n", float64(len(downloadedData))/1024.0)
	fmt.Printf(" SHA256:          %s\n", checksum)
}

func inspectNBTPalette(data []byte) (string, int, error) {
	payload := data
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return "", 0, err
		}
		decompressed, err := io.ReadAll(gz)
		_ = gz.Close()
		if err != nil {
			return "", 0, err
		}
		payload = decompressed
	}

	for _, enc := range []nbt.Encoding{nbt.BigEndian, nbt.NetworkLittleEndian, nbt.LittleEndian} {
		var raw map[string]any
		if err := nbt.UnmarshalEncoding(payload, &raw, enc); err == nil {
			blocks, ok := raw["blocks"].([]any)
			if !ok || len(blocks) == 0 {
				continue
			}

			// Extract version from first block
			versionStr := "unknown"
			if firstBlock, ok := blocks[0].(map[string]any); ok {
				if v, ok := firstBlock["version"].(int32); ok {
					major := (v >> 24) & 0xFF
					minor := (v >> 16) & 0xFF
					patch := (v >> 8) & 0xFF
					rev := v & 0xFF
					versionStr = fmt.Sprintf("%d.%d.%d.%d", major, minor, patch, rev)
				}
			}
			return versionStr, len(blocks), nil
		}
	}

	return "", 0, fmt.Errorf("unable to decode NBT palette structure")
}

func inspectJSONPalette(data []byte) (string, int, error) {
	var crustaceanFormat map[string][]any
	if err := json.Unmarshal(data, &crustaceanFormat); err == nil && len(crustaceanFormat) > 0 {
		return "latest (bedrock-crustaceans)", len(crustaceanFormat), nil
	}
	return "unknown", 0, fmt.Errorf("unable to decode JSON palette structure")
}
