// The image's HEALTHCHECK : healthy when its port takes a connection.
import net from "net";

const socket = net.connect(Number(process.env.PORT || 8000), "127.0.0.1", () => {
  socket.end();
  process.exit(0);
});
socket.on("error", () => process.exit(1));
setTimeout(() => process.exit(1), 4500).unref();
