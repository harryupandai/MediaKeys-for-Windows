<img src="logo.png" width="96" alt="MediaKeys logo">

# MediaKeys

*Version 1.0.0*

**Control your music and videos from anywhere on Windows, using the keyboard you already have.**

Pause Spotify while you're in a game, skip a YouTube video without leaving your document, or turn Discord down without opening the Volume Mixer. Every press shows a small card at the bottom of the screen with the song, the cover art and what just happened.

## Controls

| Keys | What they do |
|---|---|
| **Pause** | Play / pause. In Discord, games and other apps without media controls, it mutes and unmutes. |
| **← / →** | Previous / next song or video |
| **↑ / ↓** | Computer volume |
| **Shift + ↑ / ↓** | Volume of just the app you're controlling |
| **Shift + ← / →** | Switch which app you're controlling (Spotify, your browser, Discord, a game...) |
| **Ctrl + Alt + X** | Search Spotify songs and your own playlists with live results, and play one |
| **Scroll Lock** | Turn the arrow-key controls on or off, so the arrows work normally again |

## Features

- **Works with almost anything that plays sound:** Spotify, YouTube and other sites in Chrome, Edge, Firefox or Brave, plus Windows Media Player, VLC, Discord and games.
- **Now-playing cards** show the cover art, title and artist, tinted with the album's colors. Skipping slides smoothly from one song to the next.
- **Per-app volume:** turn one app up or down without changing anything else.
- **Pick which app the keys control:** use Shift + ← / → to switch between apps, even when Windows has picked a different one.
- **Quick search:** press Ctrl + Alt + X from anywhere and a search bar opens where the cards appear. Songs and your own Spotify playlists show up with covers as you type, and picking one starts playing it in Spotify. You stay in whatever app you were using. Before you type, it shows the songs you recently searched for and played. Each letter you type makes a soft key click, which you can turn off. AltGr on European keyboards won't trigger it by accident.
- **Smart skipping:** → skips to the next song or video. When a site has no "next", it jumps to the end of the video so autoplay can take over.
- **Light:** a single .exe with no installer and no extra services or helpers running in the background.

## Getting started

1. Download **MediaKeys-1.0.0.zip** from the [Releases](../../releases) page and extract it.
2. Double-click it. A welcome screen explains the controls and lets you choose your settings.
3. MediaKeys keeps running in the system tray by the clock. Click its icon anytime to see the controls or change settings. Right-click it for quick options or to quit.

**"Windows protected your PC"?** That screen appears for apps from small developers that aren't code-signed. Click **More info → Run anyway**.

## Settings

- **Arrow keys control media:** turn this off to use the arrow keys normally. Scroll Lock switches it too.
- **Start with Windows:** opens MediaKeys automatically when you sign in.
- **Show on-screen cards:** the popup with the song, cover art and volume.
- **Typing sounds:** a soft key click for each letter you type in the search bar.

## Good to know

- An app only appears in the Shift + ← / → list after it has made some sound. That's how the Windows Volume Mixer works.
- For browsers, Shift + ↑ / ↓ changes the whole browser's volume, because Windows gives each app one volume.
- Games or apps running **as administrator** can block MediaKeys while they're the window in front. Running MediaKeys as administrator too fixes that.
- Opening MediaKeys.exe again while it's running just brings up its window.
- Your playlists are read from the **Your Library** sidebar in the Spotify app, so they appear after Spotify has been open with the sidebar showing. With a long library, more are picked up over time as you scroll. Private playlists show a playlist icon instead of their cover.
- To start a song from the search bar, MediaKeys presses Spotify's own Play button using Windows accessibility, the same system screen readers use. If it can't, for example while Spotify is still starting up, Spotify stays open on that song's search so you can press play.

## Privacy

MediaKeys doesn't collect anything and has no account or tracking. Song info and cover art for the cards come from Windows itself, the same information shown in the Windows volume panel.

MediaKeys only uses the internet for the Ctrl + Alt + X search bar. What you type is sent to two free public music catalogs, [Apple's iTunes Search](https://performance-partners.apple.com/search-api) and [Deezer](https://developers.deezer.com/api), to show live results, because Spotify doesn't let apps like this search its catalog without each user logging in. To show the covers of your playlists, MediaKeys also asks [Spotify's public link-preview service](https://developer.spotify.com/documentation/embeds/reference/oembed) for each playlist's picture, once. Nothing else is ever sent.

Settings, your playlist names and covers, and the list of songs you've searched for and played are stored only on your PC, in `%APPDATA%\MediaKeys`.

## Uninstalling

1. Turn off **Start with Windows**, if you turned it on.
2. Right-click the tray icon and choose **Quit MediaKeys**.
3. Delete **MediaKeys.exe** and, if you like, the `%APPDATA%\MediaKeys` folder.

## Requirements

Windows 10 or 11, 64-bit.

## Building from source

MediaKeys is written in Go and uses only Go's standard library and Windows' built-in system files. See [BUILD.md](BUILD.md) for the one-line build command and what each file does.
