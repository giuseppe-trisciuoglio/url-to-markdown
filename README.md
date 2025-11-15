# url-to-markdown

Strumento CLI scritto in Go che scarica una pagina web e la converte in un documento Markdown.

## Requisiti

- Go 1.21 o superiore

## Utilizzo

```bash
go run ./cmd/url2md [-v] <url>
```

Esempio:

```bash
go run ./cmd/url2md -v https://springdoc.org
```

Produce il file `springdoc_org.md` con il contenuto della pagina convertito in Markdown.

Se il sito protegge i contenuti con tecniche anti-bot (ad esempio Cloudflare) e risponde con `403 Forbidden`, lo strumento effettua un tentativo secondario passando da `https://r.jina.ai/` per recuperare comunque il contenuto. In questo caso il testo arriva già in Markdown e viene salvato così com'è. Se il proxy risponde con un errore (`401`/`451`), puoi impostare una chiave API fornita da Jina come variabile d'ambiente `JINA_API_KEY` per autorizzare la richiesta.

## Test

```bash
go test ./...
```
