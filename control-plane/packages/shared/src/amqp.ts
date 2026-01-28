import amqplib, { Channel, ChannelModel } from "amqplib";

let conn: ChannelModel | null = null;
let ch: Channel | null = null;

export async function getAmqpChannel(): Promise<Channel> {
  if (ch) return ch;
  const url = process.env.AMQP_URL!;
  conn = await amqplib.connect(url);
  ch = await conn.createChannel();

  // Backpressure: max unacked msg per consumer
  await ch.prefetch(10);

  return ch;
}

export async function closeAmqp(): Promise<void> {
  try { await ch?.close(); } catch {}
  try { await conn?.close(); } catch {}
  ch = null;
  conn = null;
}
