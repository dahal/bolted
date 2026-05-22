# Changelog

## [0.1.0](https://github.com/dahal/bolted/compare/v0.0.1...v0.1.0) (2026-05-22)


### Features

* **dev,exec:** wire spec-18 trust gate into bolt dev/exec ([a766602](https://github.com/dahal/bolted/commit/a766602d0d67fc25f1765ef786f7c2ef0efd7140))
* implement backend interface (02), config & state (03), build & release (21) ([b2e0012](https://github.com/dahal/bolted/commit/b2e0012fe321c910cb9f38b4db63d2af1f8d60bd))
* implement CLI skeleton (spec 01) + VM image build (spec 07) ([c3c4908](https://github.com/dahal/bolted/commit/c3c4908db5aa983e3ac2400641d19cdad4f75c9e))
* implement encrypted volume lifecycle (spec 08) ([0f9a22e](https://github.com/dahal/bolted/commit/0f9a22e489058eef70598455eb4d22f919fa641f))
* implement init/unlock/lock orchestration (spec 09) ([97f2088](https://github.com/dahal/bolted/commit/97f2088d5eece25b057e5a81ad676b1699ce0b7e))
* implement Lima (spec 05) + WSL2 (spec 06) backends ([b2a12d3](https://github.com/dahal/bolted/commit/b2a12d35804b17d1012e3d929576d8666d5825f7))
* implement repo lifecycle (13) + bolted.yaml/provision (15) ([662ea2e](https://github.com/dahal/bolted/commit/662ea2e9a4d611c8428a87a5ee5f96b4b0a8baa1))
* implement resource sizing (spec 04) ([f2ca490](https://github.com/dahal/bolted/commit/f2ca49049b501b4389c703ac38c8091daa50c6db))
* implement specs 14, 16, 18, 19, 20, 22 ([c43f658](https://github.com/dahal/bolted/commit/c43f65853f68c1f8f6c03a4792adc7b30c5a81a9))
* implement status/shell (10), passthrough (11), devcontainer (12), password mgmt (17) ([d8354e0](https://github.com/dahal/bolted/commit/d8354e02cd86912431ffd1c2fc4b51429627cf8b))
* **init:** run backend preflight before prompting for password ([f5cf60d](https://github.com/dahal/bolted/commit/f5cf60d377e91db777fb9b647d321b59137231dd))
* **password:** add bolt password alias for bolt passwd ([b2ea497](https://github.com/dahal/bolted/commit/b2ea497750f2c3c8b3f704d552feed4a1abcd30c))


### Bug Fixes

* **config:** create ~/.bolted in Save so first-run bolt init succeeds ([99ab180](https://github.com/dahal/bolted/commit/99ab1804b8242c06dd3e9e648298a49a7b06990f))
* **hostinfo:** bind GlobalMemoryStatusEx by hand on Windows ([33b7858](https://github.com/dahal/bolted/commit/33b7858c11de8444328a205b034913b751583672))
* **install:** validate resolved tag and drop self-symlink ([1c84f1b](https://github.com/dahal/bolted/commit/1c84f1bebd4d5ef781aaa5a8a75a464189bb5b5b))
* **lima:** disable bundled containerd in rendered lima.yaml ([4e2b421](https://github.com/dahal/bolted/commit/4e2b421a24f78d232c50f602bcdc633d74246254))
* **lima:** drop mountType: none from rendered lima.yaml ([30b86e3](https://github.com/dahal/bolted/commit/30b86e3e7a74c781571006f7fd4913ab57e1f5c7))
* **lima:** provision spec-07 tools into the VM on first boot ([27bebc5](https://github.com/dahal/bolted/commit/27bebc5ef6be7c612a726c2205ac36b99e75d377))
* **lima:** surface limactl stderr in exitError.Error() ([8820f00](https://github.com/dahal/bolted/commit/8820f008d9bb4557f853c10165b18f39e04233fb))
* **unlock:** chown volume mountpoint so passthrough can write ([2fb60f2](https://github.com/dahal/bolted/commit/2fb60f28469878c79af018c99f017fb5e1123644))
* **volume:** run operations as root via new ExecOpts.Sudo ([3dade38](https://github.com/dahal/bolted/commit/3dade386b680e36c7dd05485881579fd0ea8e432))


### Refactor

* **cmd:** tighten cmd/bolt entrypoint and tests ([48fba0f](https://github.com/dahal/bolted/commit/48fba0f72a7f2e44a3aadc3184f6b187fcd9c785))
* **password:** promote bolt password to primary, alias passwd ([ba10142](https://github.com/dahal/bolted/commit/ba1014268da34e1f99d224b535eceae2a9dd84c1))


### Documentation

* **commands:** order CLI reference by use, not alphabet ([046dba9](https://github.com/dahal/bolted/commit/046dba94aaa594a9d7a0acd368e18515c46fdf9b))
* **developers:** architecture page ([577497c](https://github.com/dahal/bolted/commit/577497cd9c08018522ab068ae8cb7b7ac42bd35a))
* **developers:** backend abstraction page ([ca7e6f0](https://github.com/dahal/bolted/commit/ca7e6f0f3e2694e567419c37c542fe3e8f843b0e))
* **developers:** code patterns page ([842ba49](https://github.com/dahal/bolted/commit/842ba491808c6e7ae3de795cce1fc9f45132bfef))
* **developers:** contributing page ([c4cf716](https://github.com/dahal/bolted/commit/c4cf716b3bd04a67a2c292ab7c9b1aa035aac414))
* **developers:** escape &lt;goos&gt; in backend-abstraction table ([9c2d2a6](https://github.com/dahal/bolted/commit/9c2d2a6e26fe1d4087b4536325b228e4b15217fc))
* **developers:** security model page ([4560e2f](https://github.com/dahal/bolted/commit/4560e2f778ee7574bea248d6b8a2fef580a67a81))
* **developers:** testing strategy page ([e3e2eb9](https://github.com/dahal/bolted/commit/e3e2eb93034f15bb43e024827ede1a15df48429f))
* document conventional-commits, lefthook, release flow ([a2da59b](https://github.com/dahal/bolted/commit/a2da59b86e57a69a5e36a53f1c1821c73bcaa90a))
* point the docs landing at the developers section ([37da642](https://github.com/dahal/bolted/commit/37da6422d8674d37b2004c550c3420d304b1d0b8))
* **readme:** add elevator pitch + Responsible use section ([8efd70b](https://github.com/dahal/bolted/commit/8efd70b97ca661e09bd2394caa318ea9a259b5b1))
* **readme:** lead with supply-chain reality ([6a67d76](https://github.com/dahal/bolted/commit/6a67d7621358cdccd128ecdcb8cf9720f340f4a4))
* **readme:** rewrite for security-minded teams ([784368e](https://github.com/dahal/bolted/commit/784368ef77a3fec06e326ded13f0fce78d198f07))
* scaffold developers section + problem-and-solution page ([fcc1330](https://github.com/dahal/bolted/commit/fcc133078e0ee80bb5f6761a0aea35772afb08de))


### Build & Tooling

* add lefthook + golangci-lint for the pre-commit loop ([93a62b8](https://github.com/dahal/bolted/commit/93a62b8dbd69598588122ffac77c4c153f802ec5))
* **docs:** rebuild from clean fumadocs-app scaffold; add oxfmt ([6c71044](https://github.com/dahal/bolted/commit/6c71044434e7f1b525202be908a40866ec8fde5e))
* **docs:** switch from bun to pnpm with supply-chain age gates ([3623253](https://github.com/dahal/bolted/commit/36232531855c0f65aa784b2e828781ade25e8b84))
