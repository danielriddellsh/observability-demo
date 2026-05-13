FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/worker /worker
EXPOSE 9090
USER nonroot:nonroot
ENTRYPOINT ["/worker"]
