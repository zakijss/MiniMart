# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.6.0] - 2026-11-19

### Added

- Ports lists the owner's own games first: Endless Sky, then Armiger once its repository has a release. Their rows say "By ZJSS · signed".
- Signed ports: an entry marked `signed` installs or updates only if its zip's minisign signature (`X.minisig` in the same release) verifies with the owner's release key. It is checked after the SHA-256 and before anything is unpacked; a missing or bad signature refuses the install and writes nothing.
- A port that updates itself names the file holding its version (`versionfile`). Updates takes the newer of that and MiniMart's own record, so an update the game already applied isn't offered again, and an older release is never offered as an update.
- `tools/catalog`: `ports-own.json` lists the owner's games. A signed entry is pinned only to a release zip that has its `.minisig`; an entry whose repository doesn't exist yet is left out.

### Changed

- A port may hold up to 50,000 files (Endless Sky has over 22,000), and `tools/catalog` fetches zips up to 1 GB.
- A port's or game's download needs room for itself only; the port's page already checks the room the whole install takes. Icon packs, unpacked beside their zip, still need three times their size.
- Built with MiniDeck 1.3.0, for its minisign checks.
- Versions with a pre-release part order as semver does: 1.0.0-rc1 < 1.0.0-rc2 < 1.0.0-rc10 < 1.0.0 (the part after "-" was ignored).
- The installed ports' records are read once, and each port's list of files is kept in its own file, read only to update or remove it: the Ports screen no longer reads a big game's list of over 22,000 files at every redraw. Older records are moved over the first time.
- `tools/catalog`: the owner's own repositories are left out of the community apps (their apps are MiniDeck's), and repositories excluded by the owner are left out of every list.
- The built-in ports list and community apps list are regenerated.

## [1.5.1] - 2026-11-19

### Fixed

- After the all-apps zip was unzipped by hand over apps MiniMart had installed, Updates offered every app again: it believed its own older record over the app's `version.txt`. It now takes the newer of the two.

## [1.5.0] - 2026-11-19

### Added

- **A** on Updates shows what changed since the installed version, from the release's `CHANGELOG.md`, before it updates.
- Downloads show their speed and the time left.
- An installed app's page says how much room it takes on the card.

### Fixed

- An app update that fails part way (a full card, a zip without the app's `config.json`) no longer leaves a mix of old and new files: every new file is written beside the installed one first and only then renamed into place, and the room it needs is checked before anything is written.
- A file larger than 256 MB in an app's zip is refused instead of being cut short without an error.
- A zip that holds the same file twice, or two names that differ only in case (one file on the card), is refused before anything is written, instead of half updating the app.
- Temporary `.store-new` files left in an app's folder by a power cut during an update are removed at the next install or update.
- Downloads reach the card before they are renamed into place, so power lost just after a theme download can't leave an empty zip.

## [1.4.1] - 2026-11-12

### Changed

- Reaches the WireGuard and NetBird networks MiniScale 1.2 can join, through its proxy (MiniDeck 1.1.0).

## [1.4.0] - 2026-11-12

### Added

- **START** on Apps and Ports downloads the catalogues again now, instead of showing the copy on the card first, and keeps the selected row. If a download fails, it says so.
- **L2**/**R2** jump to the first or last row of every list.
- When themes, icon packs, apps, games or ports can't be loaded, **A** tries again.

### Changed

- Updates no longer says everything is up to date when some repositories couldn't be checked: it says so and offers to try again.
- The Apps footer says **Y** About, to make room for **START** Refresh.
- Built with Go 1.27.4, with updated dependencies.

## [1.3.0] - 2026-11-08

### Added

- The community list can take an app built to replace one of Onion's own apps: `hints.json` gives it a folder of its own (`folder`), so it installs beside the original instead of over it, with any facts for its page (`facts`). First use: ksielick's Battery Monitor V2, installed as `App/BatteryMonitorV2`.

### Changed

- Not-enough-space errors say to make room in MiniTop › Storage.
- An empty theme or icon pack list says to turn on a theme repository.
- The store's reminder names MiniDeck instead of MiyooNet.
- The Repositories texts mention games and icon packs.
- Grey and red small text is easier to read (MiniDeck 1.0.3).

## [1.2.0] - 2026-11-08

### Added

- The Silky Remix SUPER GAME BOY++ theme and its icon pack are listed, from a built-in repository, `zakijss/SuperGameBoy`, laid out like OnionUI/Themes. It can be turned off in Repositories like the others.

## [1.1.1] - 2026-11-03

### Security

- Theme and icon pack previews larger than 2500x2500 pixels are refused before they are decoded, so a hostile picture can't exhaust the device's memory.
- An icon pack zip may unpack to at most 256 MB in all (it was 256 MB per file, cut short without an error).

## [1.1.0] - 2026-11-02

### Added

- Ports install from MiniMart. 25 of the 72 ports in the MiyooMini-Ports list have a release laid out for Onion's Ports list; `tools/catalog` pins each one (zip and SHA-256) and publishes `ports.json` with the community apps every day. MiniMart unpacks only `Roms/PORTS/{Games,Imgs,Shortcuts,Manuals}`, updates and removes what it installed, and leaves files you added (game data, saves) in place. Ports whose release is an Onion app open in Apps.
- Ports of commercial games install the engine only. MiniMart runs Onion's own check for the game's data file, so the game appears in Onion's Ports list once your files are in `Roms/PORTS/Games/<game>/`, and says which file it is waiting for.
- Updates on the home screen: lists every installed MiniDeck app, community app and port with a newer version (installed and available version, download size). A updates one; Y updates all of them after asking once, with a progress bar each and a list of what was and wasn't updated. MiniMart itself goes last. MiniMart also checks in the background when it starts, at most once a day, and the Updates row shows how many are waiting ("5 new") and when it last checked.
- Free space on the card on the home screen, the Ports list, and every app, theme, game and port page, with the space an install needs; a port is refused before downloading when it won't fit.

### Changed

- The built-in "MiyooMini Ports" source reads MiniMart's `ports.json` (the upstream list plus pinned releases), with a copy built in for when it can't be reached. A saved on/off setting for the old address carries over.
- Requires MiniDeck 1.0.1 (on the keyboard, B on an empty field goes back).

## [1.0.0] - 2026-11-02

### Added

- The first release: themes, icon packs, MiniDeck and community apps with checkable facts and a due-diligence warning, homebrew games, and your own sources.
- Sorting by name, newest, size and, for apps, category (X in each list), remembered between runs; apps can be shown one category at a time.
- An app's README for the pinned version on its page, as plain text, kept on the card.
- The community apps list is built by `tools/catalog` from GitHub and published every day on the `catalog` release; MiniMart falls back to the copy built into it.
- Ports that need your own game file: MiniMart finds a matching copy on the card or in your own game files folder, or lets you pick one, and copies it into place. It never downloads game data.

[Unreleased]: https://github.com/zakijss/MiniMart/compare/v1.6.0...HEAD
[1.6.0]: https://github.com/zakijss/MiniMart/compare/v1.5.1...v1.6.0
[1.5.1]: https://github.com/zakijss/MiniMart/compare/v1.5.0...v1.5.1
[1.5.0]: https://github.com/zakijss/MiniMart/compare/v1.4.1...v1.5.0
[1.4.1]: https://github.com/zakijss/MiniMart/compare/v1.4.0...v1.4.1
[1.4.0]: https://github.com/zakijss/MiniMart/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/zakijss/MiniMart/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/zakijss/MiniMart/compare/v1.1.1...v1.2.0
[1.1.1]: https://github.com/zakijss/MiniMart/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/zakijss/MiniMart/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/zakijss/MiniMart/releases/tag/v1.0.0
