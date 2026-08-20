# The Android app

An APK you install yourself. It is not on the Play Store and is not meant to
be: there is one server, one person deploying it, and no reason to involve a
review queue in getting a new build onto a phone.

The app is the website with the frontend brought along. The scripts, styles,
fonts and shell live inside the APK; the account — `/sync`, `/login`,
`/account` — comes from the server over the network. That split is the whole
design, and it is what makes the next paragraph true.

## It does not update itself

`make deploy` updates the website. Every browser picks that up on the next
reload; no phone does. An installed app changes when somebody installs a new
one, and at no other time.

This is deliberate, and it is why the app is not a Trusted Web Activity — a TWA
is a shortcut to the live site, so deploying would change the app underneath
whoever installed it.

The practical consequence: **the app and the server have to stay compatible
across versions.** The API the app calls is `/sync`, `/account`, `/auth/*` and
the sign-in pages. Changing any of those in a way old clients cannot follow
will break phones running last month's build until they install this month's.

## Publishing a release

The version lives in `app.version` at the repository root, and nowhere else:

```
VERSION = 1.0.0
ORIGIN  = https://neuroscribe.augustogunsch.com
```

Edit `VERSION`, then:

```sh
make app-version            # what is in app.version right now
make app-bundle             # freeze the frontend into the project
make app-debug              # an APK to try, signed with a throwaway key
make app-release            # the real one, signed with your key
make app-publish            # build it and put it on the server
```

`make app-publish` copies the APK to `downloads/` on the server and writes the
version beside it. The landing page then offers it, named after that version.
It is deliberately not part of `make deploy`: deploying the website must never
change what is on a phone, and publishing an app is the one act that does.

The integer Android orders installs by is derived from the name — 1.2.3 becomes
10203 — so it cannot drift from it, and it always goes up.

## Signing

Make a key once and keep it:

```sh
keytool -genkeypair -v -keystore ~/.neuroscribe/release.jks \
        -alias neuroscribe -keyalg RSA -keysize 4096 -validity 10000
```

Then tell the build where it is:

```sh
export NEUROSCRIBE_KEYSTORE=~/.neuroscribe/release.jks
export NEUROSCRIBE_KEYSTORE_PASSWORD=…
export NEUROSCRIBE_KEY_ALIAS=neuroscribe          # optional, this is the default
export NEUROSCRIBE_KEY_PASSWORD=…                 # optional, defaults to the store password
```

Nothing about the key is written into the repository.

**Back the keystore up.** Android identifies an app by its signature. If that
key is lost, nobody can install a new version over this one — they have to
uninstall first, and uninstalling takes the notes on that phone with it.

## Building it

Needs a JDK 17 and the Android SDK (command-line tools are enough; Gradle
downloads the rest). If `gradlew` is not in `android/`, generate the wrapper
once with a local Gradle:

```sh
cd android && gradle wrapper --gradle-version 8.7
```

`make app-debug` produces an installable APK without a keystore, signed with
the debug key Android generates. It cannot be upgraded to a release build in
place — different signature — so it is for trying things, not for keeping.

### Building in a container

Works, with one catch worth knowing before you lose an afternoon to it: Google
ships `aapt2` only for linux-x86_64, so an **arm64 container cannot build at
all**. On an Apple Silicon machine the failure is a wall of

```
AAPT2 aapt2-…-linux Daemon #1: Daemon startup failed
This should not happen under normal circumstances, please file an issue if it does.
```

which says nothing about architecture. Pass `--platform linux/amd64` and it
builds under emulation:

```sh
docker run --rm --platform linux/amd64 \
  -v ng-sdk:/sdk -v ng-gradle:/home/gradle/.gradle \
  -v "$PWD":/w -w /w/android gradle:8.7-jdk17 sh -c '
    export ANDROID_HOME=/sdk PATH=/sdk/cmdline-tools/latest/bin:$PATH
    …install cmdline-tools, accept licences, sdkmanager platforms;android-34…
    gradle --no-daemon assembleDebug'
```

The named volumes matter: without them every run re-downloads the SDK.

Natively on macOS there is no such problem — `aapt2` ships for darwin — so a
local Android SDK is the faster path if you have one.

## What is frozen and what is not

Frozen, from the APK:

- the shell every address renders into
- `/static/**` — scripts, stylesheet, fonts, the sandboxed code runner
- `/strings/*.json` — the interface's own text

Live, from the server:

- `/sync`, `/sync/blob/*` — the notes
- `/login`, `/register`, `/verify`, `/auth/*` — signing in, which cannot work
  offline anyway, and whose pages carry server state (whether sign-ups are
  open, what went wrong last time)
- `/account` — the plan and its usage
- `/pyodide/**`, `/typst/**` — 149 MB of optional runtimes that have no business
  in a download

An asset the bundle does not have falls through to the server rather than
failing, so a sign-in page that has learned to ask for a new file still works
on an older app.

## Device identity

The app gives the frontend something a browser cannot: an installation id that
survives the WebView's storage being cleared. It lives in the app's private
`SharedPreferences`, and the PIN lock uses it to recognise this device. See the
comment at the top of `static/lock.js`.

Uninstalling loses it, which is the right boundary — a fresh install is a fresh
device, and it adopts the account's PIN on its first sign-in.

## Trying it without a phone

The split the app implements can be run on a laptop: serve
`android/app/src/main/assets/web` for the shell, `/static/` and `/strings/`, and
proxy everything else to the server. If that works, the app works — it is the
same frontend, the same origin rules and the same requests.
