# Go 側のすべてのバイナリを 1 つのイメージに入れる。
# compose では同じイメージを command 違いで起動する (ビルドは 1 回で済む)。

FROM golang:1.26 AS build
WORKDIR /src

# go.work は使わない。各モジュールの replace で解決するので、
# 評価ハーネス (eval) を含めずに済み、イメージも小さくなる。

# 依存の解決だけ先にやってレイヤキャッシュを効かせる。
COPY pkg/go.mod pkg/
COPY orchestrator/go.mod orchestrator/
COPY services/go.mod services/go.sum services/
COPY bff/go.mod bff/go.sum bff/
RUN --mount=type=cache,target=/go/pkg/mod \
    cd services && go mod download && cd ../bff && go mod download

COPY pkg/ pkg/
COPY orchestrator/ orchestrator/
COPY services/ services/
COPY bff/ bff/

ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd services && go build -trimpath -o /out/ ./cmd/... \
 && cd ../bff && go build -trimpath -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
# カタログは実行時に相対パスで読むので同梱する。
COPY catalog/ /app/catalog/
COPY --from=build /out/ /usr/local/bin/
USER nonroot
