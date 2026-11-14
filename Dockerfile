FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/entra-id-applications-exporter ./cmd/entra-id-applications-exporter

# distroless/static ships CA certificates (needed for HTTPS to Microsoft Graph)
# and runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/entra-id-applications-exporter /entra-id-applications-exporter
EXPOSE 9102
ENTRYPOINT ["/entra-id-applications-exporter"]