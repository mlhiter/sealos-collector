FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/sealos-collector ./cmd/sealos-collector \
  && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/openstatus-sync ./cmd/openstatus-sync

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sealos-collector /sealos-collector
COPY --from=build /out/openstatus-sync /openstatus-sync
USER nonroot:nonroot
ENTRYPOINT ["/sealos-collector"]
