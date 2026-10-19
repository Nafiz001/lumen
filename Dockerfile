# syntax=docker/dockerfile:1
#
# lumen serving a crawled site. On first start, with no index on the volume,
# it crawls LUMEN_SEED (politely: robots.txt, a delay between requests, one
# host) and keeps the index; later starts serve it straight away. Delete the
# volume to crawl again.
#
#   docker build -t lumen .
#   docker run -p 8080:8080 -v lumen-data:/data -e LUMEN_SEED=https://go.dev/blog/ lumen

FROM golang:1-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lumen ./cmd/lumen

FROM alpine:3
RUN adduser -S -u 10001 -h /data lumen && mkdir -p /data && chown lumen /data
COPY --from=build /out/lumen /usr/local/bin/lumen
COPY <<'EOF' /usr/local/bin/lumen-start
#!/bin/sh
set -e
if [ ! -d /data/index ]; then
  echo "lumen: no index yet, crawling $LUMEN_SEED (up to $LUMEN_MAX_PAGES pages)"
  lumen crawl "$LUMEN_SEED" -index /data/index -max-pages "$LUMEN_MAX_PAGES"
fi
set -- serve -index /data/index -addr 0.0.0.0:8080
# Crawl the seed again every LUMEN_REFRESH (0 turns it off) and swap the new
# index in without a restart; a failed or suspiciously small crawl is ignored.
if [ -n "$LUMEN_REFRESH" ] && [ "$LUMEN_REFRESH" != "0" ]; then
  set -- "$@" -refresh "$LUMEN_REFRESH" -seed "$LUMEN_SEED" -max-pages "$LUMEN_MAX_PAGES"
fi
exec lumen "$@"
EOF
RUN chmod 755 /usr/local/bin/lumen-start
ENV LUMEN_SEED=https://go.dev/blog/ \
    LUMEN_MAX_PAGES=400 \
    LUMEN_REFRESH=168h
USER lumen
VOLUME /data
EXPOSE 8080
CMD ["lumen-start"]
