# Matterbox: Mattermost in je terminal

Door wat frustraties met de officiële client, heb ik zelf maar een Mattermost client gebouwd. Matterbox is een Mattermost-client voor de terminal. Channels, DM's, threads, reactions, attachments, alles zit er in. Helemaal geoptimaliseerd voor je keyboard en echte Emico performance. 

Wat erin zit:

- Lokale cache. Elk bericht dat je ziet gaat een SQLite-database in. Een kanaal heropenen is daardoor instant en je history is offline doorzoekbaar. Er is een optionele SQL-tab als je er zelf op wilt querien.
- Full-text search over die cache (FTS5), zowel in de TUI als vanuit je shell.
- Feed-tab, met alle ongelezen berichten uit kanalen en DM's bij elkaar.
- Inline images en gifs. Via het Kitty graphics protocol worden afbeeldingen, geanimeerde gifs en custom emoji ín het transcript getekend. Giphy-links klappen vanzelf open. Video-playback zit er ook in, maar dat is echt nog experimenteel. Werkt op Ghostty, kitty en WezTerm.
- Jira en GitLab. Druk op `v` bij een issue of MR-link en je krijgt een side panel met status, pipeline en approvals. Velden aanpassen, approven of mergen kan inline.
- Een CLI die je kunt scripten. Lezen, sturen en zoeken vanuit je shell of je scripts, met `--json`-output en shell completion.
- listen-daemon met rules engine. Draait op de achtergrond, houdt je cache warm en kan op inkomende berichten reageren: match op kanaal, auteur, tekst of mention en doe er iets mee. Notificatie, lokaal commando, webhook, terugposten, reaction plaatsen, als gelezen markeren. De standaardregel stuurt je mentions en DM's door naar Telegram, waar je ook weer kunt antwoorden.
- AI-features (summaries, semantic search). Optioneel en standaard uit. Ze draaien tegen een lokaal LLM, dus er verlaat geen data je machine.

En nog veel meer natuurlijk. Kleinde disclaimer, kan goed zijn dat je nog wel wat bugs tegenkomt, maar @jasperkuiper (mede contributor) en ik draaien het al even.

Er zit ook een easter egg in. Meer zeggen we niet, maar kom je een 🦍 tegen in een draadje, reageer dan vooral. 😉

Zelf kijken: https://matterbox.work/

Vragen of bugs? [#DevTalk - Matterbox](https://chat.emico.io/emicoextern/channels/matterbox) of https://www.github.com/cornedor/matterbox.