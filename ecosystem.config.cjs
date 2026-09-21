const ports = require("./__ports.cjs");

module.exports = {
  apps: [
    {
      name: "web-app",
      cwd: __dirname,
      script: "packages/web/src/__server.ts",
      interpreter: "bun",
      interpreter_args: ["--env-file=.env"],
      exec_mode: "fork",
      instances: 1,
      autorestart: true,
      restart_delay: 1000,
      env: {
        PORT: ports.website,
      },
    },
  ],
};
