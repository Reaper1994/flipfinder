# flipfinder

Watches eBay UK for underpriced small electronics (PS5, Mac mini, Switch OLED, Steam Deck…), checks each candidate with **Jev**, and sends the best ones to **Telegram**.

It uses eBay's official Browse API, so there's no scraping and no account risk. Facebook Marketplace isn't included: it has no API and automated access breaks Meta's terms.

```
💷 PS5 console  ~£130 profit (68% ROI)
Playstaion 5 console digital + 2 games
Buy £190.00 → resell ~£320
✅ looks genuine
⏱ auction ends in 1h34m, 2 bid(s) — final price will be higher
🔤 misspelled title — fewer buyers will find it
Open on eBay
```

## How it decides

1. **Search** each target's normal queries plus deliberate misspellings (`playstaion 5`, `mac mni m2`), newest first, UK only, excluding "for parts".
2. **Price rules** (free, instant): drop excluded words (box only, iCloud locked…), anything too cheap to be the real item, anything above `max_buy_ratio × market_price`, auctions not ending within 3 hours or with too many bids. Postage counts toward cost.
3. **Jev check**, only for survivors (so a few calls per run). Four typed questions in one request, same pattern as Sift:
   - `matches`: is it the target model, not an older one?
   - `genuine`: the complete working item, not a box/part/wanted ad?
   - `risk`: scam, stolen, locked or banned signals?
   - `condition`: 0 faulty … 3 like new
4. **`Decide`** in `jev.go` turns the answers into keep/drop plus a score. Tune thresholds there; each case has a test.
5. **Rank** by profit and ROI, send the top few, remember them in `seen.json` so you get each deal once (again only if the price drops 5%+).

## Try it now (no keys)

```bash
go run . -demo      # fictional listings, simulated Jev, prints to terminal
go test ./...
```

## Set up

**1. eBay API keys (free).** Sign up at developer.ebay.com, create an app, and copy the **Production** App ID (Client ID) and Cert ID (Client Secret). Default limit is 5,000 calls/day; this uses about 20 per run.

   To test with **Sandbox** keys (their App ID contains `-SBX-`), also set `EBAY_ENV=sandbox`. The sandbox only holds eBay's test listings, so expect few or no deals: it proves the keys and setup work, not the deal finding.

**2. Telegram bot (free).** Message @BotFather → `/newbot` → copy the token. Send your bot any message, then open `https://api.telegram.org/bot<TOKEN>/getUpdates` and copy `chat.id`.

**3. Jev (optional but recommended).** Use your TypeSafe key. Without it, only the price rules run.

**4. Set your prices.** Edit `market_price` in `watchlist.json` for each target using recent eBay **sold** listings or CeX. The numbers in the file are placeholders; every profit figure depends on them, so revisit them every few weeks.

```bash
export EBAY_CLIENT_ID=... EBAY_CLIENT_SECRET=...
export TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=...
export TYPESAFE_API_KEY=...          # optional
go run . -dry -v                     # print deals and skip reasons, send nothing
go run .                             # send to Telegram
go run . -every 15m                  # keep running (on a VPS)
```

## Run it in the cloud for free (GitHub Actions)

1. Push this folder to a **private** GitHub repo.
2. Repo → Settings → Secrets and variables → Actions → add `EBAY_CLIENT_ID`, `EBAY_CLIENT_SECRET`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `TYPESAFE_API_KEY`.
   For sandbox keys, also add a **variable** (Variables tab, not Secrets) `EBAY_ENV` = `sandbox`.
3. Actions tab → **flipfinder** → **Run workflow** to test. "Dry run" is ticked by default: deals and skip reasons go to the run log and nothing is sent to Telegram, so the Telegram secrets aren't needed for that test.

It then runs every 30 minutes from 7am to midnight UK time, about 1,100 of your 2,000 free monthly minutes. GitHub can delay scheduled runs by a few minutes at busy times, and pauses schedules on repos with no activity for 60 days (push a commit to wake it). For faster checks, run `-every 10m` on your existing VPS instead.

## Flags

| Flag | Default | |
|---|---|---|
| `-config` | `watchlist.json` | targets and rules |
| `-state` | `seen.json` | alerted-listing memory |
| `-demo` | off | sample data, no keys |
| `-dry` | off | print instead of Telegram; reads `seen.json` but never updates it |
| `-every` | run once | e.g. `15m` to loop |
| `-max` | 8 | alerts per run |
| `-v` | off | log why listings were skipped |
| `-ping` | off | test Telegram and the Jev key without searching eBay, then exit |

## Before you buy

Alerts are leads, not guarantees. Ask the seller for a photo showing the item powered on, check Activation Lock is off on Macs and that consoles aren't PSN-banned, and always pay through eBay.
