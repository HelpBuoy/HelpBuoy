# Microsoft Store listing

The Store listing of the Windows client (Store ID 9NZXVS50R8VB), in the languages of the package (`installer/helpbuoy.appxmanifest.xml`). Partner Center asks for these per language under Store listings.

    listing/<language>/name.txt         Product name, the name reserved in Partner Center
    listing/<language>/description.txt  Description, at most 10,000 characters
    listing/<language>/features.txt     Product features, one per line, at most 20 of at most 200 characters
    listing/<language>/whatsnew.txt     What's new in this version, at most 1,500 characters; empty for the first submission
    screenshots/<language>/*.png        Screenshots, see below

The languages are en-us, nl-nl, de-de, fr-fr, es-es and it-it. The texts use the same words as the app's translations (`proto/i18n/locales`): "techie" is Techniker, technicien, técnico and tecnico, and the buttons are named as the app names them. German and French use the formal Sie and vous, like the app.

`msstore publish` copies the listing of the previous submission, so a change here goes into Partner Center by hand (or through its listing import), and `whatsnew.txt` with it for every release.

## Screenshots

Take them on Windows 11 from the installed package, not in Wine: the Store shows them as what users get, and Wine draws the window in its own theme. Use a clean desktop at 1920x1080 with the HelpBuoy window in the middle, save them as PNG, and take one set per language (the app follows the Windows display language). Use a test helpdesk with made-up details, for example the company "Example Helpdesk" and the phone number +1 555 0100, so no real names or numbers end up in the Store.

1. `1-address.png`: the address screen with the server address typed in.
2. `2-key.png`: the key of this PC, ready to copy and send to the techie.
3. `3-waiting.png`: "Waiting for a techie", with the company and phone number of the helpdesk.
4. `4-allow.png`: "A techie wants to connect." with Allow and Not now.
