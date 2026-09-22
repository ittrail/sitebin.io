// The container E2E's app: two ports, a MySQL query through the project
// network, and one hostile act -- a symlink out of its volume that Sitebin
// must never follow.
const http = require("http");
const fs = require("fs");
const mysql = require("mysql2/promise");

try { fs.symlinkSync("/data", "leak"); } catch (e) { /* already there */ }

let pool;
async function db() {
  if (!pool) {
    pool = mysql.createPool({
      host: process.env.DB_HOST, port: Number(process.env.DB_PORT),
      user: process.env.DB_USER, password: process.env.DB_PASSWORD, database: process.env.DB_NAME,
    });
  }
  const [rows] = await pool.query("SELECT VERSION() AS v");
  return rows[0].v;
}

http.createServer(async (req, res) => {
  try {
    const v = await db();
    res.end(`greeting=${process.env.GREETING} mysql=${v} uid=${process.getuid()}\n`);
  } catch (e) {
    res.statusCode = 503;
    res.end("db not ready: " + e.code + "\n");
  }
}).listen(3000);

http.createServer((req, res) => res.end("second port\n")).listen(3001);
