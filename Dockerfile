# Use the official Golang image to create a build artifact.

FROM golang:1.18-alpine AS builder


WORKDIR /app


COPY go.mod go.sum ./
RUN go mod download


COPY . .




RUN CGO_ENABLED=0 GOOS=linux go build -o app ./...


FROM alpine:latest

WORKDIR /root/

# Copy the Pre-built binary file from the previous stage
COPY --from=builder /app/app .

# Copy the static and templates folders
COPY --from=builder /app/static ./static
COPY --from=builder /app/templates ./templates

# Expose port 8080 to the outside world
EXPOSE 8080

# Command to run the executable
CMD ["./app"]