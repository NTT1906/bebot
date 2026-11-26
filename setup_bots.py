#!/usr/bin/env python3
"""
setup_bots.py - Multi-Bot Local Setup & Launcher for Bebot

Creates and configures multiple local/offline Bedrock bots in isolated directories:
    bots/{bot_name}/
Each bot folder contains its own config, executables, run scripts, and local cache/log isolation.

Usage examples:
    python setup_bots.py --count 5
    python setup_bots.py --count 3 --start
    python setup_bots.py --start
    python setup_bots.py --stop
    python setup_bots.py --status
    python setup_bots.py --clean --count 4
"""

import argparse
import json
import os
import random
import shutil
import subprocess
import sys
import time
from pathlib import Path

# Word banks for generating thematic, distinct Bedrock player names (<= 16 chars)
ADJECTIVES = [
    "Iron", "Swift", "Rusty", "Frost", "Shadow", "Silent", "Golden", "Storm",
    "Brave", "Crafty", "Turbo", "Pixel", "Hyper", "Blaze", "Neon", "Cyber",
    "Cosmic", "Vortex", "Quick", "Steel", "Atomic", "Cobble", "Echo", "Prism"
]

NOUNS = [
    "Miner", "Crafter", "Bot", "Scout", "Golem", "Guard", "Walker", "Knight",
    "Ranger", "Runner", "Builder", "Digger", "Striker", "Hunter", "Pilot", "Sentry"
]


def generate_random_name(existing_names: set) -> str:
    """Generate a unique random player name conforming to Minecraft length (<= 16 chars)."""
    for _ in range(1000):
        adj = random.choice(ADJECTIVES)
        noun = random.choice(NOUNS)
        num = random.randint(10, 99)
        candidate = f"{adj}{noun}_{num}"
        if len(candidate) <= 16 and candidate not in existing_names:
            existing_names.add(candidate)
            return candidate

    # Fallback with timestamp if space is saturated
    candidate = f"Bot_{int(time.time()) % 100000:05d}"
    existing_names.add(candidate)
    return candidate


def get_existing_bot_dirs(bots_dir: Path) -> list[str]:
    """Return sorted (alphabetical) list of existing valid bot folder names."""
    if not bots_dir.exists():
        return []
    names = []
    for p in bots_dir.iterdir():
        if p.is_dir() and not p.name.startswith("."):
            if (p / "run.bat").exists() or (p / "config.json").exists() or (p / "bebot.exe").exists():
                names.append(p.name)
    names.sort(key=lambda s: s.lower())
    return names


def load_json(path: Path) -> dict:
    if not path.exists():
        return {}
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def save_json(path: Path, data: dict):
    with open(path, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2)


def place_executable(src_exe: Path, dst_exe: Path, method: str):
    """Place the executable into the bot folder using the requested method."""
    if dst_exe.exists():
        try:
            # Skip if destination binary is already up-to-date
            if dst_exe.stat().st_size == src_exe.stat().st_size and dst_exe.stat().st_mtime >= src_exe.stat().st_mtime:
                return
            dst_exe.unlink()
        except OSError:
            pass

    if method == "link":
        try:
            os.link(src_exe, dst_exe)
            return
        except OSError as e:
            print(f"  [warn] Hardlink failed ({e}), falling back to copy.")

    try:
        shutil.copy2(src_exe, dst_exe)
    except PermissionError:
        print(f"  [info] {dst_exe.name} is currently running in this bot folder; using existing binary.")


def setup_single_bot(
    bot_name: str,
    bot_dir: Path,
    src_exe: Path,
    src_config: Path,
    method: str,
    host: str,
    port: int,
    superuser: str | None = None
) -> dict:
    """Setup an isolated environment for one bot."""
    bot_dir.mkdir(parents=True, exist_ok=True)

    # 1. Place executable
    dst_exe = bot_dir / "bebot.exe"
    if method != "shared":
        place_executable(src_exe, dst_exe, method)
        exe_cmd = "bebot.exe"
    else:
        # Use relative path pointing to the root bebot.exe
        rel_exe = os.path.relpath(src_exe, bot_dir)
        exe_cmd = rel_exe

    # 2. Prepare tailored config.json (Local & Offline)
    config_data = load_json(src_config)
    if "server" not in config_data:
        config_data["server"] = {}
    config_data["server"]["host"] = host
    config_data["server"]["port"] = port
    config_data["commands_via_chat"] = True

    if superuser:
        config_data["superuser"] = superuser

    save_json(bot_dir / "config.json", config_data)

    # 3. Copy skin files if present in root
    skin_json = src_config.parent / "skin.json"
    if skin_json.exists():
        shutil.copy2(skin_json, bot_dir / "skin.json")

    skin_folder = src_config.parent / "skin"
    if skin_folder.is_dir():
        dst_skin_folder = bot_dir / "skin"
        if not dst_skin_folder.exists():
            shutil.copytree(skin_folder, dst_skin_folder)

    # 4. Generate batch launcher (run.bat)
    run_bat = bot_dir / "run.bat"
    with open(run_bat, "w", encoding="utf-8") as f:
        f.write(f"@echo off\r\n")
        f.write(f"title Bebot - {bot_name}\r\n")
        f.write(f'"{exe_cmd}" -local -offline -name "{bot_name}" -config config.json\r\n')
        f.write(f"pause\r\n")

    # 5. Generate PowerShell launcher (run.ps1)
    run_ps1 = bot_dir / "run.ps1"
    with open(run_ps1, "w", encoding="utf-8") as f:
        f.write(f'$host.UI.RawUI.WindowTitle = "Bebot - {bot_name}"\r\n')
        f.write(f'& ".\\{exe_cmd}" -local -offline -name "{bot_name}" -config config.json\r\n')

    return {
        "name": bot_name,
        "dir": str(bot_dir),
        "run_bat": str(run_bat),
        "host": host,
        "port": port
    }


def generate_master_scripts(bots_dir: Path, bot_info_list: list):
    """Generate start_all.bat and stop_all.bat in bots/ directory."""
    start_all = bots_dir / "start_all.bat"
    with open(start_all, "w", encoding="utf-8") as f:
        f.write("@echo off\r\n")
        f.write("echo Starting all local bots...\r\n")
        for b in bot_info_list:
            name = b["name"]
            f.write(f'start "Bebot - {name}" /D "%~dp0{name}" bebot.exe -local -offline -name "{name}" -config config.json\r\n')
            f.write("timeout /t 1 /nobreak >nul\r\n")
        f.write("echo All bots launched!\r\n")

    stop_all = bots_dir / "stop_all.bat"
    with open(stop_all, "w", encoding="utf-8") as f:
        f.write("@echo off\r\n")
        f.write("echo Stopping all bebot processes...\r\n")
        f.write("taskkill /F /IM bebot.exe /T\r\n")
        f.write("echo Done.\r\n")


def start_bots(bots_dir: Path, bot_names: list[str] | None = None, src_exe: Path | None = None, delay: float = 1.0):
    """Launch configured bots in background windows, updating binaries if newer."""
    manifest_file = bots_dir / "manifest.json"
    manifest = load_json(manifest_file) if manifest_file.exists() else {}
    manifest_bots = [b["name"] for b in manifest.get("bots", [])]

    if not bot_names:
        if manifest_bots:
            bot_names = manifest_bots
        else:
            bot_names = get_existing_bot_dirs(bots_dir)

    if not bot_names:
        print(f"[error] No bots found in {bots_dir}. Run setup first.")
        return

    # Check and sync latest bebot.exe into bot dirs if newer
    if src_exe and src_exe.exists():
        src_mtime = src_exe.stat().st_mtime
        for name in bot_names:
            dst_exe = bots_dir / name / "bebot.exe"
            if dst_exe.exists() and dst_exe.stat().st_mtime < src_mtime:
                try:
                    shutil.copy2(src_exe, dst_exe)
                    print(f"  [*] Updated bebot.exe for '{name}' to latest build.")
                except Exception:
                    pass

    print(f"[*] Launching {len(bot_names)} bots (delay: {delay}s between launches)...")
    pids = []
    for name in bot_names:
        b_dir = bots_dir / name
        bat_file = b_dir / "run.bat"
        if not bat_file.exists():
            print(f"  [skip] {bat_file} not found.")
            continue

        print(f"  -> Launching bot '{name}'...")
        proc = subprocess.Popen(
            ["cmd.exe", "/c", str(bat_file.resolve())],
            cwd=str(b_dir.resolve()),
            creationflags=subprocess.CREATE_NEW_CONSOLE
        )
        pids.append({"name": name, "pid": proc.pid})
        time.sleep(delay)

    save_json(bots_dir / ".pids.json", pids)
    print("[+] All selected bots launched successfully!")


def stop_bots():
    """Stop all running bebot.exe processes."""
    print("[*] Terminating bebot processes...")
    try:
        subprocess.run(["taskkill", "/F", "/IM", "bebot.exe", "/T"], capture_output=True, text=True)
        print("[+] Bebot processes terminated.")
    except Exception as e:
        print(f"[error] Failed to stop bots: {e}")


def check_status(bots_dir: Path):
    """Check running status of bots."""
    manifest_file = bots_dir / "manifest.json"
    if not manifest_file.exists():
        print(f"[*] No bot setups found in {bots_dir}.")
        return

    manifest = load_json(manifest_file)
    bots = manifest.get("bots", [])
    print(f"[*] Configured bots ({len(bots)} total in {bots_dir}):")

    # Query running processes
    try:
        out = subprocess.check_output(
            ["tasklist", "/FI", "IMAGENAME eq bebot.exe", "/FO", "CSV"],
            text=True
        )
        lines = [l.strip() for l in out.splitlines() if "bebot.exe" in l.lower()]
        running_count = len(lines)
    except Exception:
        running_count = 0

    for idx, b in enumerate(bots, 1):
        print(f"  {idx}. {b['name']} -> {b['dir']}")

    print(f"\n[*] Active bebot.exe processes detected on system: {running_count}")


def main():
    parser = argparse.ArgumentParser(
        description="Setup and launch multiple local/offline Bedrock bots in isolated directories."
    )
    parser.add_argument(
        "--exe",
        type=Path,
        default=Path("bebot.exe"),
        help="Path to source bebot.exe (default: ./bebot.exe)"
    )
    parser.add_argument(
        "--config",
        type=Path,
        default=Path("config.json"),
        help="Path to source config.json (default: ./config.json)"
    )
    parser.add_argument(
        "--count",
        type=int,
        default=3,
        help="Number of random bots to generate (default: 3)"
    )
    parser.add_argument(
        "--bots-dir",
        type=Path,
        default=Path("bots"),
        help="Target folder to contain bot instances (default: ./bots)"
    )
    parser.add_argument(
        "--names",
        nargs="+",
        help="Explicit list of bot names (overrides random generation)"
    )
    parser.add_argument(
        "--host",
        default="127.0.0.1",
        help="Server host address (default: 127.0.0.1)"
    )
    parser.add_argument(
        "--port",
        type=int,
        default=19132,
        help="Server port (default: 19132)"
    )
    parser.add_argument(
        "--superuser",
        help="Superuser gamertag to set in bot configs"
    )
    parser.add_argument(
        "--method",
        choices=["copy", "link", "shared"],
        default="copy",
        help="How to place bebot.exe: copy (full copy), link (hardlink), shared (run central exe)"
    )
    parser.add_argument(
        "--clean",
        action="store_true",
        help="Clean up existing bots directory before generating"
    )
    parser.add_argument(
        "--new",
        action="store_true",
        help="Force creating new bot profiles instead of reusing existing ones in bots/"
    )
    parser.add_argument(
        "--start",
        action="store_true",
        help="Launch bots after setup"
    )
    parser.add_argument(
        "--stop",
        action="store_true",
        help="Stop/kill all running bebot.exe processes"
    )
    parser.add_argument(
        "--status",
        action="store_true",
        help="Show status of configured and running bots"
    )
    parser.add_argument(
        "--delay",
        type=float,
        default=1.0,
        help="Seconds delay between launching each bot (default: 1.0)"
    )

    args = parser.parse_args()

    # Action: Stop
    if args.stop:
        stop_bots()
        return

    # Action: Status
    if args.status:
        check_status(args.bots_dir)
        return

    # Action: Clean
    if args.clean:
        if args.bots_dir.exists():
            print(f"[*] Cleaning up {args.bots_dir}...")
            try:
                shutil.rmtree(args.bots_dir)
                print(f"[+] Removed {args.bots_dir}.")
            except PermissionError:
                print(f"[warn] Could not remove all files in {args.bots_dir} (bots are currently running).")
                print("       Run 'python setup_bots.py --stop' first, then clean.")
        else:
            print(f"[*] {args.bots_dir} does not exist.")
        # If user ONLY ran --clean (without explicit --count, --start, or --names), exit here
        clean_only = not any(a in sys.argv for a in ["--count", "--start", "--names", "--method"])
        if clean_only:
            return

    # Validate source files
    src_exe = args.exe.resolve()
    if not src_exe.exists():
        print(f"[error] Executable not found: {src_exe}")
        print("        Please build bebot first (go build -o bebot.exe .) or specify --exe path.")
        sys.exit(1)

    src_config = args.config.resolve()
    if not src_config.exists():
        print(f"[error] Config file not found: {src_config}")
        sys.exit(1)

    args.bots_dir.mkdir(parents=True, exist_ok=True)

    # Determine bot selection: reuse existing sorted alphabetically unless --new or not enough
    existing_bots = get_existing_bot_dirs(args.bots_dir)
    used_names = set(existing_bots)

    if args.names:
        bot_names = args.names
    elif args.new or not existing_bots:
        bot_names = [generate_random_name(used_names) for _ in range(args.count)]
    else:
        # Reuse existing bots, sorted alphabetically
        needed = args.count
        if len(existing_bots) >= needed:
            bot_names = existing_bots[:needed]
            print(f"[*] Reusing first {len(bot_names)} existing bots (alphabetical): {', '.join(bot_names)}")
        else:
            bot_names = list(existing_bots)
            missing = needed - len(bot_names)
            print(f"[*] Found {len(bot_names)} existing bots: {', '.join(bot_names)}")
            print(f"[*] Generating {missing} new bot(s) to reach requested count of {needed}...")
            for _ in range(missing):
                bot_names.append(generate_random_name(used_names))

    print(f"[*] Target bots ({len(bot_names)} total): {', '.join(bot_names)}")
    print(f"[*] Configuring in '{args.bots_dir}' (Method: {args.method}, Target: {args.host}:{args.port})...")

    bot_info_list = []
    for name in bot_names:
        b_dir = args.bots_dir / name
        info = setup_single_bot(
            bot_name=name,
            bot_dir=b_dir,
            src_exe=src_exe,
            src_config=src_config,
            method=args.method,
            host=args.host,
            port=args.port,
            superuser=args.superuser
        )
        bot_info_list.append(info)
        print(f"  [+] Ready: {name} -> {b_dir}")

    # Generate master launch & stop scripts in bots/
    generate_master_scripts(args.bots_dir, bot_info_list)

    # Save manifest
    manifest_data = {
        "updated_at": time.strftime("%Y-%m-%d %H:%M:%S"),
        "method": args.method,
        "host": args.host,
        "port": args.port,
        "bots": bot_info_list
    }
    save_json(args.bots_dir / "manifest.json", manifest_data)

    print(f"[+] Configured {len(bot_names)} bots successfully!")
    print(f"    - Launch all: {args.bots_dir / 'start_all.bat'}")
    print(f"    - Stop all:   {args.bots_dir / 'stop_all.bat'}")

    # Launch if requested
    if args.start:
        start_bots(args.bots_dir, bot_names=bot_names, src_exe=src_exe, delay=args.delay)


if __name__ == "__main__":
    main()
