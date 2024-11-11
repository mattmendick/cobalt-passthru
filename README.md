# cobalt-passthru
Exposes a really simple server to proxy/cache cobalt downloaded videos

# Building docker image
`docker build -t cobalt-passthru .`

This builds the docker image which gets referenced in the Dockerfile

# Steps
1. Check out this project onto a host
2. Install docker
3. Build the docker image
4. Run `docker compose up -d` (the `-d` detaches)
5. Request `http://{ip-of-machine}/u?=https://www.tiktok.com/t/ZTFTVyuoy/` with a browser or a <video> tag in a webpage
6. Get the video served to you. If you hit it again it will serve the video and the original headers from the cache located in the storage folder. The videos are cleared out after they're 12h old, and it scans the directory for old files every 10 minutes.

# What's it even for?
This project uses the imputnet/cobalt project to actually do the heavy lifting of getting the video and downloading/caching/serving it. The public API kindly provided by cobalt stopped streaming videos so it became harder to serve a video and put the resulting cobalt API video in a <video> tag. So this let's you do that again.

# Scale?
It probably doesn't scale very well, and probably has issues handling two concurrent requests for the same video gracefully. The first one will write the file and then begin serving it out, while the second will try to write the same file again, wasting a bit of work.

# Did I write it?
Mostly no. I make heavy use of ChatGPT to do the heavy lifting for me on boilerplate things like http servers and cli parameters and some of the logic.