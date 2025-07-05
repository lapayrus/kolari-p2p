# Use the official Golang image to create a build artifact.

FROM golang:1.18-alpine AS builder


WORKDIR /app


COPY go.mod go.sum ./
RUN go mod download


COPY . .




RUN CGO_ENABLED=0 GOOS=linux go build -o app ./...


FROM alpine:latest

WORKDIR /root/


COPY --from=builder /app/app .


COPY --from=builder /app/static ./static
COPY --from=builder /app/templates ./templates


EXPOSE 8080


CMD ["./app"]