# Microsoft Store listing

The Store listing of the Windows client (Store ID 9NZXVS50R8VB), in the languages of the package (`installer/helpbuoy.appxmanifest.xml`). Partner Center asks for these per language under Store listings.

    listing/<language>/name.txt         Product name, the name reserved in Partner Center
    listing/<language>/description.txt  Description, at most 10,000 characters
    listing/<language>/features.txt     Product features, one per line, at most 20 of at most 200 characters
    listing/<language>/whatsnew.txt     What's new in this version, at most 1,500 characters; empty for the first submission
    screenshots/<language>/*.png        Screenshots, see below
    logos/logo-<size>.png               Store logos, the 1:1 app tile icon at 300x300, 150x150 and 71x71

The languages are en-us, nl-nl, de-de, fr-fr, es-es and it-it. The texts use the same words as the app's translations (`proto/i18n/locales`): "techie" is Techniker, technicien, técnico and tecnico, and the buttons are named as the app names them. German and French use the formal Sie and vous, like the app.

`msstore publish` copies the listing of the previous submission, so a change here goes into Partner Center by hand (or through its listing import), and `whatsnew.txt` with it for every release.

## Logos

The logos are the app icon from `tools/genicon`, drawn at 88% of the size and centered on a transparent square, so the rope keeps some room from the edge of the tile. To make them again:

    for n in 300 150 71; do
        i=$(( (n * 88 + 50) / 100 ))
        GOWORK=off GO111MODULE=off go run tools/genicon/main.go bin/logo-$i.png $i
        convert bin/logo-$i.png -background none -gravity center -extent ${n}x${n} msstore/logos/logo-${n}x${n}.png
    done

## Screenshots

`msstore/screenshots.sh` makes them on Linux, for all languages or the ones given, in about half a minute per language. It runs the real client in Wine against the fake server, made up to look like Windows 11 at 200% (see the top of the script for how), and saves 1920x1080 PNGs. The PC is anna@ANNA-PC and the helpdesk "Example Helpdesk" with phone number +1 555 0100, so no real names or numbers end up in the Store. Run it again after a change to the client's window or texts.

1. `1-connect.png`: the address screen with the server address typed in.
2. `2-warning.png`: the warning on the first connection to a server.
3. `3-key.png`: the key of this PC, ready to copy and send to the techie.
4. `4-lock.png`: the offer to get help only through this helpdesk.
5. `5-waiting.png`: "Waiting for a techie", with the company and phone number of the helpdesk.
6. `6-allow.png`: a techie asks to take control, with Allow and Cancel.

Wine differs from Windows 11 in the controls themselves: buttons and fields have square corners, the checkbox is not filled and the focused field has no blue underline. The link at the bottom gets a few pixels of extra underline.
