# syntax=docker/dockerfile:1
# A static binary on distroless.
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
COPY go.mod go.sum ./
RUN go mod download
COPY VERSION ./
COPY cmd ./cmd
COPY internal ./internal
RUN version=$(sed 's/ .*//' VERSION) \
 && go build -trimpath -ldflags "-s -w -X main.version=${version}" -o /out/redgreen ./cmd/redgreen \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/redgreen /app/redgreen
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV PORT=8080 DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/app/redgreen"]
CMD ["serve"]
