# Building MediaKeys

MediaKeys is written in Go and uses only Go's standard library plus Windows' own system DLLs.
There is no installer and nothing else to download.

## Build

1. Install Go 1.24 or newer from https://go.dev/dl/
2. Open a terminal in this folder and run:

   ```
   go build -trimpath -ldflags "-H windowsgui -s -w" -o MediaKeys.exe .
   ```

   From macOS or Linux, cross-compile with:

   ```
   GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H windowsgui -s -w" -o MediaKeys.exe .
   ```

`-H windowsgui` makes it a background app with no console window; `-s -w` makes the file smaller.

## Icon and version info

`rsrc_windows_amd64.syso` holds the app icon and the version details shown in the file's Properties.
Go picks it up automatically. To change them, edit `app.ico` or `app.rc` and rebuild it with:

```
windres -i app.rc -o rsrc_windows_amd64.syso -O coff --target=pe-x86-64
```

(`llvm-windres` works the same way.)

## What each file does

| File | What's in it |
|---|---|
| `main.go` | Starting up, the keyboard listener (all the shortcuts), the tray icon and its menu |
| `settings.go` | Saving and loading settings |
| `settingsui.go` | The welcome / Controls & Settings window |
| `osd.go` | The on-screen cards: drawing, animation, cover-art tint, slide and fade transitions |
| `smtc.go` | Reading what's playing from Windows (title, artist, cover, playing/paused), choosing the app the keys control, play/pause |
| `media.go` | The Pause key and its card, with a fallback for when Windows has no media info |
| `skip.go` | ← / → : next and previous, YouTube's next-video shortcut, skipping to the end of a video |
| `appvolume.go` | Shift + ↑ / ↓ : volume of just the app you're controlling |
| `audioapps.go` | Apps without media info (Discord, games): names, icons, mute |
| `coreaudio.go` | Windows Core Audio (the Volume Mixer) and shared COM helpers |
| `search.go` | The Ctrl + Alt + X search bar |
| `catalog.go` | Live search results (Apple iTunes Search + Deezer) and ranking the most likely song first |
| `uia.go` | Pressing Spotify's Play button through Windows accessibility (playlists start from the first song, or a random one on shuffle), and putting Spotify back how it was |
| `history.go` | The list of songs and playlists you searched for and played |
| `playlists.go` | Reading your playlists from Spotify's Your Library sidebar, their covers, renames |
| `sound.go`, `clicksynth.go` | The typing click, generated in code |
| `monitor.go` | Finding the screen you're using |
| `log.go` | The small error log in `%APPDATA%\MediaKeys` |
