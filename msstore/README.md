# Microsoft Store

Everything Partner Center asks for, for both apps in the Store:

| App | Folder | Package | Package family name | Store ID |
| --- | --- | --- | --- | --- |
| HelpBuoy, the Windows client | `helpbuoy/` | `installer/helpbuoy.appxmanifest.xml` | MaussoftBV.HelpBuoy_d55rzzeqj6ck6 | 9NZXVS50R8VB |
| HelpBuoy Desk, the desk app | `helpbuoy-desk/` | `installer/helpbuoy-desk.appxmanifest.xml` | MaussoftBV.HelpBuoyDesk_d55rzzeqj6ck6 | 9NM475B9T4BC |

A Store app keeps the data it writes to AppData in `%LOCALAPPDATA%\Packages\<package family name>`, which Windows removes with the app.

`make msix` builds both packages in `bin/`, unsigned, for the upload; `make msix-test` also signs a copy of each with a self-signed test certificate, to install on a test PC first.

Each app folder has:

    listing/<language>/name.txt         Product name, the name reserved in Partner Center
    listing/<language>/description.txt  Description, at most 10,000 characters
    listing/<language>/features.txt     Product features, one per line, at most 20 of at most 200 characters
    listing/<language>/whatsnew.txt     What's new in this version, at most 1,500 characters; empty for the first submission
    screenshots/<language>/*.png        Screenshots, see below
    logos/logo-<size>.png               Store logos, the 1:1 app tile icon at 300x300, 150x150 and 71x71

The languages are en-us, nl-nl, de-de, fr-fr, es-es and it-it, those of the packages. The texts use the same words as the apps' translations (`proto/i18n/locales`): "techie" is Techniker, technicien, técnico and tecnico, a computer is an equipo in Spanish, and buttons are named as the apps name them. German and French use the formal Sie and vous, like the apps.

`msstore publish` copies the listing of the previous submission, so a change here goes into Partner Center by hand (or through its listing import), and `whatsnew.txt` with it for every release.

`reference/` holds real Windows 11 screenshots of both apps, which the screenshots are measured against.

## Publishing updates

`release.sh` submits every release to the Store by itself, through `msstore/publish.sh`, which can also be run by hand (`msstore/publish.sh 0.5.4`, or with `helpbuoy` or `helpbuoy-desk` after the version for one app). It uses the Microsoft Store Developer CLI, `msstore`, which takes the last published submission of each app, with its listing, screenshots and pricing, puts in the new package and sends it to certification. That takes from hours to a few days; the Store then updates the installed apps. While an update is still being certified, the next one is refused for that app, and the release script says so and how to submit it later.

Changes to the listing, screenshots or logos in this folder still go into Partner Center by hand.

Setting up, once:

1. Submit the first version of each app by hand in Partner Center; the CLI can only update an app that has a published submission.
2. Install the CLI: `MSStoreCLI-linux-x64.tar.gz` from https://github.com/microsoft/msstore-cli/releases holds one self-contained `msstore` executable (no .NET runtime needed); put it somewhere in your PATH, such as `~/.local/bin`. It sends telemetry by default; `msstore settings --enableTelemetry false` turns that off.
3. In Partner Center, under Account settings, User management, Microsoft Entra applications, add an application with the Manager role and make a client secret for it. Note the tenant ID, the client ID and the secret, and the seller ID from Account settings, Legal info.
4. Fill in `msstore/credentials.env`, which git ignores and only you can read (`credentials.env.example` says where each value is in Partner Center). `publish.sh` configures `msstore` from it before every submission, and turns off its telemetry. `msstore/publish.sh --check` checks the credentials without submitting anything: it should list both apps.

The client secret expires (after at most two years). When it has, `msstore` fails to log in: make a new secret in Partner Center and put it in `msstore/credentials.env`.

## Logos

Both apps have the same icon. The logos are that icon from `tools/genicon`, drawn at 88% of the size and centered on a transparent square, so the rope keeps some room from the edge of the tile. To make them again:

    for n in 300 150 71; do
        i=$(( (n * 88 + 50) / 100 ))
        GOWORK=off GO111MODULE=off go run tools/genicon/main.go bin/logo-$i.png $i
        convert bin/logo-$i.png -background none -gravity center -extent ${n}x${n} msstore/helpbuoy/logos/logo-${n}x${n}.png
    done
    cp msstore/helpbuoy/logos/*.png msstore/helpbuoy-desk/logos/

## Screenshots

`msstore/screenshots.sh` makes them on Linux, for both apps or one, for all languages or the ones given (`msstore/screenshots.sh helpbuoy-desk nl-nl`), in about three minutes for everything. They are 1920x1080 PNGs with the window in a Windows 11 frame; the top of the script says how. Run it again after a change to a window or its texts, or after a release, since the titles show the version.

The client runs for real in Wine against the fake server, at 200%. The PC is anna@ANNA-PC and the helpdesk "Example Helpdesk" with phone number +1 555 0100, so no real names or numbers end up in the Store.

1. `1-connect.png`: the address screen with the server address typed in.
2. `2-warning.png`: the warning on the first connection to a server.
3. `3-key.png`: the key of this PC, ready to copy and send to the techie.
4. `4-lock.png`: the offer to get help only through this helpdesk.
5. `5-waiting.png`: "Waiting for a techie", with the company and phone number of the helpdesk.
6. `6-allow.png`: a techie asks to take control, with Allow and Cancel.

Wine differs from Windows 11 in the controls themselves: buttons and fields have square corners, the checkbox is not filled and the focused field has no blue underline. The link at the bottom gets a few pixels of extra underline.

The desk app draws its own window, the same on every system, so its Linux build makes the pictures in demo mode, at 150%:

1. `1-computers.png`: the computers, per customer.
2. `2-session.png`: a session with the demo's test picture.
3. `3-server.png`: the built-in server and the helpdesk details.
4. `4-register.png`: registering a computer by its key.
