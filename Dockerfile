# Build: docker build -t wikiwright .
# Run:  docker run --rm -p 43123:43123 \
#         -v /etc/wikiwright/wikiwright.yaml:/etc/wikiwright/wikiwright.yaml:ro \
#         -v wikiwright-data:/var/lib/wikiwright \
#         wikiwright
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -o /out/wikiwright ./cmd/wikiwright

FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --home /var/lib/wikiwright --create-home wikiwright
COPY --from=build /out/wikiwright /usr/local/bin/wikiwright
USER wikiwright
WORKDIR /var/lib/wikiwright
EXPOSE 43123
ENTRYPOINT ["wikiwright"]
CMD ["serve", "-config", "/etc/wikiwright/wikiwright.yaml"]
