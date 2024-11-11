# Start with the official golang image
FROM golang:1.21-alpine AS builder

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download needed modules (the dependencies)
RUN go mod download

# Copy the source code into the container
COPY . .

# Build the Go app
RUN go build -o cobalt-passthru

# Use a minimal Alpine Linux image to reduce final image size
FROM alpine:latest

WORKDIR /root/

# Copy the binary from the builder container to this new phase
COPY --from=builder /app/cobalt-passthru .

# Expose the port the server listens on
EXPOSE 9001

# Command to run the executable
CMD ["./cobalt-passthru", "-endpoint=http://localhost:9000", "-addr=:9001", "-storage=./storage"]