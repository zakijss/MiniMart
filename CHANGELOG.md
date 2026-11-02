# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.0] - 2026-11-02

### Added

- The first release: themes, icon packs, MiniDeck and community apps with checkable facts and a due-diligence warning, homebrew games, and your own sources.
- Sorting by name, newest, size and, for apps, category (X in each list), remembered between runs; apps can be shown one category at a time.
- An app's README for the pinned version on its page, as plain text, kept on the card.
- The community apps list is built by `tools/catalog` from GitHub and published every day on the `catalog` release; MiniMart falls back to the copy built into it.
- Ports that need your own game file: MiniMart finds a matching copy on the card or in your own game files folder, or lets you pick one, and copies it into place. It never downloads game data.

[Unreleased]: https://github.com/zakijss/MiniMart/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/zakijss/MiniMart/releases/tag/v1.0.0
