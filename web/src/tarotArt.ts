import tarotBack from "./assets/cards/tarot-back.webp";
import tarotHunter from "./assets/cards/tarot-hunter.webp";
import tarotSeer from "./assets/cards/tarot-seer.webp";
import tarotVillager from "./assets/cards/tarot-villager.webp";
import tarotWerewolf from "./assets/cards/tarot-werewolf.webp";
import tarotWitch from "./assets/cards/tarot-witch.webp";
import type { Role } from "./types";

export const TAROT_BACK_IMAGE = tarotBack;

export interface RoleArcanaMeta {
  numeral: string;
  latinTitle: string;
  cnTitle: string;
  motto: string;
  image: string;
}

export const ROLE_ARCANA: Record<Role, RoleArcanaMeta> = {
  WEREWOLF: {
    numeral: "XV",
    latinTitle: "ARCANA XV · LUPUS",
    cnTitle: "暗月狼人",
    motto: "蚀月之下，血契同谋，隐于人言。",
    image: tarotWerewolf,
  },
  SEER: {
    numeral: "II",
    latinTitle: "ARCANA II · ORACULUM",
    cnTitle: "星象预言家",
    motto: "天眼洞开，星轨昭示善恶真身。",
    image: tarotSeer,
  },
  WITCH: {
    numeral: "XIV",
    latinTitle: "ARCANA XIV · ALCHEMIA",
    cnTitle: "秘药女巫",
    motto: "左执生机翠露，右掌冥渊剧毒。",
    image: tarotWitch,
  },
  HUNTER: {
    numeral: "VII",
    latinTitle: "ARCANA VII · VENATOR",
    cnTitle: "银铳猎人",
    motto: "纵陨暗夜，银弹亦必贯彻仇敌。",
    image: tarotHunter,
  },
  VILLAGER: {
    numeral: "IX",
    latinTitle: "ARCANA IX · LUCERNA",
    cnTitle: "守夜平民",
    motto: "微光聚火，以逻辑与选票守卫村落。",
    image: tarotVillager,
  },
};
